package lntest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/aezeed"
	"github.com/decred/dcrlnd/chanbackup"
	"github.com/decred/dcrlnd/internal/testutils"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/invoicesrpc"
	"github.com/decred/dcrlnd/lnrpc/routerrpc"
	"github.com/decred/dcrlnd/lnrpc/signrpc"
	"github.com/decred/dcrlnd/lnrpc/walletrpc"
	"github.com/decred/dcrlnd/lnrpc/watchtowerrpc"
	"github.com/decred/dcrlnd/lnrpc/wtclientrpc"
	"github.com/decred/dcrlnd/lntest/wait"
	"github.com/decred/dcrlnd/macaroons"
	rpctest "github.com/decred/dcrtest/dcrdtest"
	"github.com/jackc/pgx/v4/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

const (
	// logPubKeyBytes is the number of bytes of the node's PubKey that will
	// be appended to the log file name. The whole PubKey is too long and
	// not really necessary to quickly identify what node produced which
	// log file.
	logPubKeyBytes = 4

	// trickleDelay is the amount of time in milliseconds between each
	// release of announcements by AuthenticatedGossiper to the network.
	trickleDelay = 50

	postgresDsn = "postgres://postgres:postgres@localhost:6432/%s?sslmode=disable"

	// commitInterval specifies the maximum interval the graph database
	// will wait between attempting to flush a batch of modifications to
	// disk(db.batch-commit-interval).
	commitInterval = 10 * time.Millisecond
)

var (
	// numActiveNodes is the number of active nodes within the test network.
	numActiveNodes uint32 = 0
)

func postgresDatabaseDsn(dbName string) string {
	return fmt.Sprintf(postgresDsn, dbName)
}

// generateListeningPorts returns four ints representing ports to listen on
// designated for the current lightning network test. This returns the next
// available ports for the p2p, rpc, rest and profiling services.
func generateListeningPorts(cfg *NodeConfig) {
	if cfg.P2PPort == 0 {
		cfg.P2PPort = NextAvailablePort()
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = NextAvailablePort()
	}
	if cfg.RESTPort == 0 {
		cfg.RESTPort = NextAvailablePort()
	}
	if cfg.ProfilePort == 0 {
		cfg.ProfilePort = NextAvailablePort()
	}
	if cfg.WalletPort == 0 {
		cfg.WalletPort = NextAvailablePort()
	}
}

// BackendConfig is an interface that abstracts away the specific chain backend
// node implementation.
type BackendConfig interface {
	// GenArgs returns the arguments needed to be passed to LND at startup
	// for using this node as a chain backend.
	GenArgs() []string

	// StartWalletSync starts the sync process of a remote wallet using the
	// given backend implementation.
	StartWalletSync(loader pb.WalletLoaderServiceClient, password []byte) error

	// ConnectMiner is called to establish a connection to the test miner.
	ConnectMiner() error

	// DisconnectMiner is called to disconnect the miner.
	DisconnectMiner() error

	// Name returns the name of the backend type.
	Name() string
}

type NodeConfig struct {
	Name string

	// LogFilenamePrefix is is used to prefix node log files. Can be used
	// to store the current test case for simpler postmortem debugging.
	LogFilenamePrefix string

	BackendCfg BackendConfig
	NetParams  *chaincfg.Params
	BaseDir    string
	ExtraArgs  []string

	DataDir        string
	LogDir         string
	TLSCertPath    string
	TLSKeyPath     string
	AdminMacPath   string
	ReadMacPath    string
	InvoiceMacPath string

	HasSeed      bool
	Password     []byte
	RemoteWallet bool
	DcrwNode     bool

	P2PPort     int
	RPCPort     int
	RESTPort    int
	ProfilePort int
	WalletPort  int

	AcceptKeySend bool
	AcceptAMP     bool

	FeeURL string

	DbBackend   DatabaseBackend
	PostgresDsn string
}

func (cfg NodeConfig) P2PAddr() string {
	return fmt.Sprintf(listenerFormat, cfg.P2PPort)
}

func (cfg NodeConfig) RPCAddr() string {
	return fmt.Sprintf(listenerFormat, cfg.RPCPort)
}

func (cfg NodeConfig) RESTAddr() string {
	return fmt.Sprintf(listenerFormat, cfg.RESTPort)
}

// DBDir returns the holding directory path of the graph database.
func (cfg NodeConfig) DBDir() string {
	return filepath.Join(cfg.DataDir, "graph", cfg.NetParams.Name)
}

func (cfg NodeConfig) DBPath() string {
	return filepath.Join(cfg.DBDir(), "channel.db")
}

func (cfg NodeConfig) ChanBackupPath() string {
	return filepath.Join(
		cfg.DataDir, "chain", "decred",
		fmt.Sprintf(
			"%v/%v", cfg.NetParams.Name,
			chanbackup.DefaultBackupFileName,
		),
	)
}

// genArgs generates a slice of command line arguments from the lightning node
// config struct.
func (cfg NodeConfig) genArgs() []string {
	var args []string

	switch cfg.NetParams.Net {
	case wire.TestNet3:
		args = append(args, "--testnet")
	case wire.SimNet:
		args = append(args, "--simnet")
	}

	backendArgs := cfg.BackendCfg.GenArgs()
	args = append(args, backendArgs...)

	nodeArgs := []string{
		"--nobootstrap",
		"--debuglevel=debug",
		"--defaultchanconfs=1",
		fmt.Sprintf("--db.batch-commit-interval=%v", commitInterval),
		fmt.Sprintf("--defaultremotedelay=%v", DefaultCSV),
		fmt.Sprintf("--rpclisten=%v", cfg.RPCAddr()),
		fmt.Sprintf("--restlisten=%v", cfg.RESTAddr()),
		fmt.Sprintf("--restcors=https://%v", cfg.RESTAddr()),
		fmt.Sprintf("--listen=%v", cfg.P2PAddr()),
		fmt.Sprintf("--externalip=%v", cfg.P2PAddr()),
		fmt.Sprintf("--logdir=%v", cfg.LogDir),
		fmt.Sprintf("--datadir=%v", cfg.DataDir),
		fmt.Sprintf("--tlscertpath=%v", cfg.TLSCertPath),
		fmt.Sprintf("--tlskeypath=%v", cfg.TLSKeyPath),
		fmt.Sprintf("--configfile=%v", cfg.DataDir),
		fmt.Sprintf("--adminmacaroonpath=%v", cfg.AdminMacPath),
		fmt.Sprintf("--readonlymacaroonpath=%v", cfg.ReadMacPath),
		fmt.Sprintf("--invoicemacaroonpath=%v", cfg.InvoiceMacPath),
		fmt.Sprintf("--trickledelay=%v", trickleDelay),
		fmt.Sprintf("--profile=%d", cfg.ProfilePort),
		fmt.Sprintf("--caches.rpc-graph-cache-duration=%d", 0),
	}
	args = append(args, nodeArgs...)

	if cfg.RemoteWallet {
		args = append(args, fmt.Sprintf("--dcrwallet.grpchost=localhost:%d", cfg.WalletPort))
		args = append(args, fmt.Sprintf("--dcrwallet.certpath=%s", cfg.TLSCertPath))
		args = append(args, fmt.Sprintf("--dcrwallet.clientkeypath=%s", cfg.TLSKeyPath))
		args = append(args, fmt.Sprintf("--dcrwallet.clientcertpath=%s", cfg.TLSCertPath))
	}

	if cfg.DcrwNode {
		args = append(args, "--node=dcrw")
	}

	if !cfg.HasSeed {
		args = append(args, "--noseedbackup")
	}

	if cfg.ExtraArgs != nil {
		args = append(args, cfg.ExtraArgs...)
	}

	if cfg.AcceptKeySend {
		args = append(args, "--accept-keysend")
	}

	if cfg.AcceptAMP {
		args = append(args, "--accept-amp")
	}

	switch cfg.DbBackend {
	case BackendEtcd:
		args = append(args, "--db.backend=etcd")
		args = append(args, "--db.etcd.embedded")
		args = append(
			args, fmt.Sprintf(
				"--db.etcd.embedded_client_port=%v",
				NextAvailablePort(),
			),
		)
		args = append(
			args, fmt.Sprintf(
				"--db.etcd.embedded_peer_port=%v",
				NextAvailablePort(),
			),
		)
		args = append(
			args, fmt.Sprintf(
				"--db.etcd.embedded_log_file=%v",
				path.Join(cfg.LogDir, "etcd.log"),
			),
		)

	case BackendPostgres:
		args = append(args, "--db.backend=postgres")
		args = append(args, "--db.postgres.dsn="+cfg.PostgresDsn)
	}

	if cfg.FeeURL != "" {
		args = append(args, "--feeurl="+cfg.FeeURL)
	}

	return args
}

func (cfg *NodeConfig) genWalletArgs() []string {
	var args []string

	switch cfg.NetParams.Net {
	case wire.TestNet3:
		args = append(args, "--testnet")
	case wire.SimNet:
		args = append(args, "--simnet")
	}

	args = append(args, "--nolegacyrpc")
	args = append(args, "--noinitialload")
	args = append(args, "--debuglevel=debug")
	args = append(args, fmt.Sprintf("--grpclisten=127.0.0.1:%d", cfg.WalletPort))
	args = append(args, fmt.Sprintf("--logdir=%s", cfg.LogDir))
	args = append(args, fmt.Sprintf("--appdata=%s", cfg.DataDir))
	args = append(args, fmt.Sprintf("--rpccert=%s", cfg.TLSCertPath))
	args = append(args, fmt.Sprintf("--rpckey=%s", cfg.TLSKeyPath))
	args = append(args, fmt.Sprintf("--clientcafile=%s", cfg.TLSCertPath))

	// This is not strictly necessary, but it's useful to reduce the
	// startup time of test wallets since it prevents two address discovery
	// processes from happening.
	args = append(args, "--disablecointypeupgrades")

	return args
}

// policyUpdateMap defines a type to store channel policy updates. It has the
// format,
//
//	{
//	 "chanPoint1": {
//	      "advertisingNode1": [
//	             policy1, policy2, ...
//	      ],
//	      "advertisingNode2": [
//	             policy1, policy2, ...
//	      ]
//	 },
//	 "chanPoint2": ...
//	}
type policyUpdateMap map[string]map[string][]*lnrpc.RoutingPolicy

// HarnessNode represents an instance of lnd running within our test network
// harness. Each HarnessNode instance also fully embeds an RPC client in
// order to pragmatically drive the node.
type HarnessNode struct {
	Cfg *NodeConfig

	// NodeID is a unique identifier for the node within a NetworkHarness.
	NodeID int

	// PubKey is the serialized compressed identity public key of the node.
	// This field will only be populated once the node itself has been
	// started via the start() method.
	PubKey    [33]byte
	PubKeyStr string

	walletCmd  *exec.Cmd
	walletConn *grpc.ClientConn

	// rpc holds a list of RPC clients.
	rpc *RPCClients

	// chanWatchRequests receives a request for watching a particular event
	// for a given channel.
	chanWatchRequests chan *chanWatchRequest

	// For each outpoint, we'll track an integer which denotes the number of
	// edges seen for that channel within the network. When this number
	// reaches 2, then it means that both edge advertisements has propagated
	// through the network.
	openChans        map[wire.OutPoint]int
	openChanWatchers map[wire.OutPoint][]chan struct{}

	closedChans       map[wire.OutPoint]struct{}
	closeChanWatchers map[wire.OutPoint][]chan struct{}

	// policyUpdates stores a slice of seen polices by each advertising
	// node and the outpoint.
	policyUpdates policyUpdateMap

	// backupDbDir is the path where a database backup is stored, if any.
	backupDbDir string

	// postgresDbName is the name of the postgres database where lnd data is
	// stored in.
	postgresDbName string

	// runCtx is a context with cancel method. It's used to signal when the
	// node needs to quit, and used as the parent context when spawning
	// children contexts for RPC requests.
	runCtx context.Context
	cancel context.CancelFunc

	wg      sync.WaitGroup
	cmd     *exec.Cmd
	logFile *os.File

	// TODO(yy): remove
	lnrpc.LightningClient
	lnrpc.WalletUnlockerClient
	invoicesrpc.InvoicesClient
	SignerClient     signrpc.SignerClient
	RouterClient     routerrpc.RouterClient
	WalletKitClient  walletrpc.WalletKitClient
	Watchtower       watchtowerrpc.WatchtowerClient
	WatchtowerClient wtclientrpc.WatchtowerClientClient
	StateClient      lnrpc.StateClient
}

// RPCClients wraps a list of RPC clients into a single struct for easier
// access.
type RPCClients struct {
	// conn is the underlying connection to the grpc endpoint of the node.
	conn *grpc.ClientConn

	LN               lnrpc.LightningClient
	WalletUnlocker   lnrpc.WalletUnlockerClient
	Invoice          invoicesrpc.InvoicesClient
	Signer           signrpc.SignerClient
	Router           routerrpc.RouterClient
	WalletKit        walletrpc.WalletKitClient
	Watchtower       watchtowerrpc.WatchtowerClient
	WatchtowerClient wtclientrpc.WatchtowerClientClient
	State            lnrpc.StateClient
}

// Assert *HarnessNode implements the lnrpc.LightningClient interface.
var _ lnrpc.LightningClient = (*HarnessNode)(nil)
var _ lnrpc.WalletUnlockerClient = (*HarnessNode)(nil)
var _ invoicesrpc.InvoicesClient = (*HarnessNode)(nil)

// nextNodeID generates a unique sequence to be used as the node's ID.
func nextNodeID() int {
	return int(atomic.AddUint32(&numActiveNodes, 1))
}

// newNode creates a new test lightning node instance from the passed config.
func newNode(cfg NodeConfig) (*HarnessNode, error) {
	if cfg.BaseDir == "" {
		var err error
		cfg.BaseDir, err = ioutil.TempDir("", "lndtest-node")
		if err != nil {
			return nil, err
		}
	}
	cfg.DataDir = filepath.Join(cfg.BaseDir, "data")
	cfg.LogDir = filepath.Join(cfg.BaseDir, "log")
	cfg.TLSCertPath = filepath.Join(cfg.DataDir, "tls.cert")
	cfg.TLSKeyPath = filepath.Join(cfg.DataDir, "tls.key")

	networkDir := filepath.Join(
		cfg.DataDir, "chain", "decred", cfg.NetParams.Name,
	)
	cfg.AdminMacPath = filepath.Join(networkDir, "admin.macaroon")
	cfg.ReadMacPath = filepath.Join(networkDir, "readonly.macaroon")
	cfg.InvoiceMacPath = filepath.Join(networkDir, "invoice.macaroon")

	generateListeningPorts(&cfg)

	err := os.MkdirAll(cfg.DataDir, os.FileMode(0755))
	if err != nil {
		return nil, err
	}

	// Run all tests with accept keysend. The keysend code is very isolated
	// and it is highly unlikely that it would affect regular itests when
	// enabled.
	cfg.AcceptKeySend = true

	// Create temporary database.
	var dbName string
	if cfg.DbBackend == BackendPostgres {
		var err error
		dbName, err = createTempPgDb()
		if err != nil {
			return nil, err
		}
		cfg.PostgresDsn = postgresDatabaseDsn(dbName)
	}

	return &HarnessNode{
		Cfg:               &cfg,
		NodeID:            nextNodeID(),
		chanWatchRequests: make(chan *chanWatchRequest),
		openChans:         make(map[wire.OutPoint]int),
		openChanWatchers:  make(map[wire.OutPoint][]chan struct{}),

		closedChans:       make(map[wire.OutPoint]struct{}),
		closeChanWatchers: make(map[wire.OutPoint][]chan struct{}),

		policyUpdates: policyUpdateMap{},

		postgresDbName: dbName,
	}, nil
}

func createTempPgDb() (string, error) {
	// Create random database name.
	randBytes := make([]byte, 8)
	_, err := rand.Read(randBytes)
	if err != nil {
		return "", err
	}
	dbName := "itest_" + hex.EncodeToString(randBytes)

	// Create database.
	err = executePgQuery("CREATE DATABASE " + dbName)
	if err != nil {
		return "", err
	}

	return dbName, nil
}

func executePgQuery(query string) error {
	pool, err := pgxpool.Connect(
		context.Background(),
		postgresDatabaseDsn("postgres"),
	)
	if err != nil {
		return fmt.Errorf("unable to connect to database: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(context.Background(), query)
	return err
}

// NewMiner creates a new miner using btcd backend. The logDir specifies the
// miner node's log dir. When tests finished, during clean up, its logs are
// copied to a file specified as logFilename.
func NewMiner(t *testing.T, logDir, logFilename string, netParams *chaincfg.Params,
	handler *rpcclient.NotificationHandlers) (*rpctest.Harness,
	func() error, error) {

	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--nobanning",
		"--debuglevel=debug",
		"--logdir=" + logDir,
		"--maxorphantx=0",
		"--rpcmaxclients=100",
		"--rpcmaxwebsockets=100",
		"--rpcmaxconcurrentreqs=100",
		"--logsize=100M",
		"--maxsameip=200",
	}

	miner, err := testutils.NewSetupRPCTest(t, 5, netParams, handler,
		args, false, 0)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"unable to create mining node: %v", err,
		)
	}

	cleanUp := func() error {
		if err := miner.TearDown(); err != nil {
			return fmt.Errorf(
				"failed to tear down miner, got error: %s", err,
			)
		}

		// After shutting down the miner, we'll make a copy of the log
		// file before deleting the temporary log dir.
		logFile := fmt.Sprintf("%s/%s/dcrd.log", logDir, netParams.Name)
		copyPath := fmt.Sprintf("%s/../%s", logDir, logFilename)
		err := CopyFile(filepath.Clean(copyPath), logFile)
		if err != nil {
			return fmt.Errorf("unable to copy file: %v", err)
		}

		if err = os.RemoveAll(logDir); err != nil {
			return fmt.Errorf(
				"cannot remove dir %s: %v", logDir, err,
			)
		}
		return nil
	}

	return miner, cleanUp, nil
}

// String gives the internal state of the node which is useful for debugging.
func (hn *HarnessNode) String() string {
	type nodeCfg struct {
		LogFilenamePrefix string
		ExtraArgs         []string
		HasSeed           bool
		P2PPort           int
		RPCPort           int
		RESTPort          int
		ProfilePort       int
		AcceptKeySend     bool
		AcceptAMP         bool
		FeeURL            string
	}

	nodeState := struct {
		NodeID      int
		Name        string
		PubKey      string
		OpenChans   map[string]int
		ClosedChans map[string]struct{}
		NodeCfg     nodeCfg
	}{
		NodeID:      hn.NodeID,
		Name:        hn.Cfg.Name,
		PubKey:      hn.PubKeyStr,
		OpenChans:   make(map[string]int),
		ClosedChans: make(map[string]struct{}),
		NodeCfg: nodeCfg{
			LogFilenamePrefix: hn.Cfg.LogFilenamePrefix,
			ExtraArgs:         hn.Cfg.ExtraArgs,
			HasSeed:           hn.Cfg.HasSeed,
			P2PPort:           hn.Cfg.P2PPort,
			RPCPort:           hn.Cfg.RPCPort,
			RESTPort:          hn.Cfg.RESTPort,
			AcceptKeySend:     hn.Cfg.AcceptKeySend,
			AcceptAMP:         hn.Cfg.AcceptAMP,
			FeeURL:            hn.Cfg.FeeURL,
		},
	}

	for outpoint, count := range hn.openChans {
		nodeState.OpenChans[outpoint.String()] = count
	}
	for outpoint, count := range hn.closedChans {
		nodeState.ClosedChans[outpoint.String()] = count
	}

	bytes, err := json.MarshalIndent(nodeState, "", "\t")
	if err != nil {
		return fmt.Sprintf("\n encode node state with err: %v", err)
	}

	return fmt.Sprintf("\nnode state: %s", bytes)
}

// DBPath returns the filepath to the channeldb database file for this node.
func (hn *HarnessNode) DBPath() string {
	return hn.Cfg.DBPath()
}

// DBDir returns the path for the directory holding channeldb file(s).
func (hn *HarnessNode) DBDir() string {
	return hn.Cfg.DBDir()
}

// Name returns the name of this node set during initialization.
func (hn *HarnessNode) Name() string {
	return hn.Cfg.Name
}

// TLSCertStr returns the path where the TLS certificate is stored.
func (hn *HarnessNode) TLSCertStr() string {
	return hn.Cfg.TLSCertPath
}

// TLSKeyStr returns the path where the TLS key is stored.
func (hn *HarnessNode) TLSKeyStr() string {
	return hn.Cfg.TLSKeyPath
}

// ChanBackupPath returns the fielpath to the on-disk channel.backup file for
// this node.
func (hn *HarnessNode) ChanBackupPath() string {
	return hn.Cfg.ChanBackupPath()
}

// AdminMacPath returns the filepath to the admin.macaroon file for this node.
func (hn *HarnessNode) AdminMacPath() string {
	return hn.Cfg.AdminMacPath
}

// ReadMacPath returns the filepath to the readonly.macaroon file for this node.
func (hn *HarnessNode) ReadMacPath() string {
	return hn.Cfg.ReadMacPath
}

// InvoiceMacPath returns the filepath to the invoice.macaroon file for this
// node.
func (hn *HarnessNode) InvoiceMacPath() string {
	return hn.Cfg.InvoiceMacPath
}

// startLnd handles the startup of lnd, creating log files, and possibly kills
// the process when needed.
func (hn *HarnessNode) startLnd(lndBinary string, lndError chan<- error) error {
	args := hn.Cfg.genArgs()
	hn.cmd = exec.Command(lndBinary, args...)

	// Redirect stderr output to buffer
	var errb bytes.Buffer
	hn.cmd.Stderr = &errb

	// If the logoutput flag is passed, redirect output from the nodes to
	// log files.
	var (
		fileName string
		err      error
	)
	if *logOutput {
		fileName, err = addLogFile(hn)
		if err != nil {
			return err
		}
	}

	if hn.Cfg.RemoteWallet {
		err := hn.startRemoteWallet()
		if err != nil {
			return fmt.Errorf("unable to start remote dcrwallet: %v", err)
		}
	}

	if err := hn.cmd.Start(); err != nil {
		return fmt.Errorf("unable to start %s's dcrlnd-itest: %v", hn.Name(), err)
	}

	// Launch a new goroutine which that bubbles up any potential fatal
	// process errors to the goroutine running the tests.
	hn.wg.Add(1)
	go func() {
		defer hn.wg.Done()

		err := hn.cmd.Wait()
		if err != nil {
			lndError <- fmt.Errorf("%v\n%v", err, errb.String())
		}

		// Make sure log file is closed and renamed if necessary.
		finalizeLogfile(hn, fileName)

		// Rename the etcd.log file if the node was running on embedded
		// etcd.
		finalizeEtcdLog(hn)
	}()

	// Do the same for remote wallet.
	if hn.walletCmd != nil {
		hn.wg.Add(1)
		go func() {
			defer hn.wg.Done()
			err := hn.walletCmd.Wait()
			if err != nil {
				lndError <- fmt.Errorf("wallet error during final wait: %v", err)
			}
		}()
	}

	return nil
}

// Start launches a new process running lnd. Additionally, the PID of the
// launched process is saved in order to possibly kill the process forcibly
// later.
//
// This may not clean up properly if an error is returned, so the caller should
// call shutdown() regardless of the return value.
func (hn *HarnessNode) start(lndBinary string, lndError chan<- error,
	wait bool) error {

	// Init the runCtx.
	ctxt, cancel := context.WithCancel(context.Background())
	hn.runCtx = ctxt
	hn.cancel = cancel

	// Start lnd and prepare logs.
	if err := hn.startLnd(lndBinary, lndError); err != nil {
		return err
	}

	// We may want to skip waiting for the node to come up (eg. the node
	// is waiting to become the leader).
	if !wait {
		return nil
	}

	// Since Stop uses the LightningClient to stop the node, if we fail to
	// get a connected client, we have to kill the process.
	useMacaroons := !hn.Cfg.HasSeed && !hn.Cfg.RemoteWallet
	conn, err := hn.ConnectRPC(useMacaroons)
	if err != nil {
		err = fmt.Errorf("ConnectRPC err: %w", err)
		cmdErr := hn.cmd.Process.Kill()
		if cmdErr != nil {
			err = fmt.Errorf("kill process got err: %w: %v",
				cmdErr, err)
		}
		return err
	}

	// Init all the RPC clients.
	hn.initRPCClients(conn)

	if err := hn.WaitUntilStarted(); err != nil {
		return err
	}

	// If the node was created with a seed, we will need to perform an
	// additional step to unlock the wallet. The connection returned will
	// only use the TLS certs, and can only perform operations necessary to
	// unlock the daemon.
	if hn.Cfg.HasSeed {
		// TODO(yy): remove
		hn.WalletUnlockerClient = lnrpc.NewWalletUnlockerClient(conn)
		return nil
	}

	if hn.Cfg.RemoteWallet {
		hn.WalletUnlockerClient = lnrpc.NewWalletUnlockerClient(conn)
		err := hn.unlockRemoteWallet()
		if err != nil {
			hn.cmd.Process.Kill()
			hn.walletCmd.Process.Kill()
			return fmt.Errorf("unable to init remote wallet: %v", err)
		}
		return nil
	}

	return hn.initLightningClient()
}

func tlsCertFromFile(fname string) (*x509.CertPool, error) {
	b, err := ioutil.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("credentials: failed to append certificates")
	}

	return cp, nil
}

func (hn *HarnessNode) startRemoteWallet() error {
	// Prepare and start the remote wallet process
	walletArgs := hn.Cfg.genWalletArgs()
	const dcrwalletExe = "dcrwallet-dcrlnd"
	hn.walletCmd = exec.Command(dcrwalletExe, walletArgs...)

	hn.walletCmd.Stdout = hn.logFile
	hn.walletCmd.Stderr = hn.logFile

	if err := hn.walletCmd.Start(); err != nil {
		return fmt.Errorf("unable to start %s's wallet: %v", hn.Name(), err)
	}

	// Wait until the TLS cert file exists, so we can connect to the
	// wallet.
	var caCert *x509.CertPool
	var clientCert tls.Certificate
	err := wait.NoError(func() error {
		var err error
		caCert, err = tlsCertFromFile(hn.Cfg.TLSCertPath)
		if err != nil {
			return fmt.Errorf("unable to load wallet tls cert: %v", err)
		}

		clientCert, err = tls.LoadX509KeyPair(hn.Cfg.TLSCertPath, hn.Cfg.TLSKeyPath)
		if err != nil {
			return fmt.Errorf("unable to load wallet cert and key files: %v", err)
		}

		return nil
	}, time.Second*30)
	if err != nil {
		return fmt.Errorf("error reading wallet TLS cert: %v", err)
	}

	// Setup the TLS config and credentials.
	tlsCfg := &tls.Config{
		ServerName:   "localhost",
		RootCAs:      caCert,
		Certificates: []tls.Certificate{clientCert},
	}
	creds := credentials.NewTLS(tlsCfg)

	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithTimeout(time.Second * 40),
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Millisecond * 20,
				Multiplier: 1,
				Jitter:     0.2,
				MaxDelay:   time.Millisecond * 20,
			},
			MinConnectTimeout: time.Millisecond * 20,
		}),
	}

	hn.walletConn, err = grpc.Dial(
		fmt.Sprintf("localhost:%d", hn.Cfg.WalletPort),
		opts...,
	)
	if err != nil {
		return fmt.Errorf("unable to connect to the wallet: %v", err)
	}

	password := hn.Cfg.Password
	if len(password) == 0 {
		password = []byte("private1")
	}

	// Open or create the wallet as necessary.
	ctxb := context.Background()
	loader := pb.NewWalletLoaderServiceClient(hn.walletConn)
	respExists, err := loader.WalletExists(ctxb, &pb.WalletExistsRequest{})
	if err != nil {
		return err
	}
	if respExists.Exists {
		_, err := loader.OpenWallet(ctxb, &pb.OpenWalletRequest{})
		if err != nil {
			return err
		}

		err = hn.Cfg.BackendCfg.StartWalletSync(loader, password)
		if err != nil {
			return err
		}
	} else if !hn.Cfg.HasSeed {
		// If the test won't require or provide a seed, then initialize
		// the wallet with a random one.
		seed, err := hdkeychain.GenerateSeed(hdkeychain.RecommendedSeedLen)
		if err != nil {
			return err
		}
		reqCreate := &pb.CreateWalletRequest{
			PrivatePassphrase: password,
			Seed:              seed,
		}
		_, err = loader.CreateWallet(ctxb, reqCreate)
		if err != nil {
			return err
		}

		// Set the wallet to use per-account passphrases.
		wallet := pb.NewWalletServiceClient(hn.walletConn)
		reqSetAcctPwd := &pb.SetAccountPassphraseRequest{
			AccountNumber:        0,
			WalletPassphrase:     password,
			NewAccountPassphrase: password,
		}
		_, err = wallet.SetAccountPassphrase(ctxb, reqSetAcctPwd)
		if err != nil {
			return err
		}

		// Wait for the wallet to be relocked. This is needed to avoid a
		// wrongful lock event during the following syncing stage.
		err = wait.NoError(func() error {
			res, err := wallet.AccountUnlocked(ctxb, &pb.AccountUnlockedRequest{AccountNumber: 0})
			if err != nil {
				return err
			}
			if res.Unlocked {
				return fmt.Errorf("wallet account still unlocked")
			}
			return nil
		}, 30*time.Second)
		if err != nil {
			return err
		}

		err = hn.Cfg.BackendCfg.StartWalletSync(loader, password)
		if err != nil {
			return err
		}
	}

	return nil
}

func (hn *HarnessNode) unlockRemoteWallet() error {
	password := hn.Cfg.Password
	if len(password) == 0 {
		password = []byte("private1")
	}

	// Assemble the client key+cert buffer for gRPC auth into the wallet.
	var clientKeyCert []byte
	cert, err := ioutil.ReadFile(hn.Cfg.TLSCertPath)
	if err != nil {
		return fmt.Errorf("unable to load wallet client cert file: %v", err)
	}
	key, err := ioutil.ReadFile(hn.Cfg.TLSKeyPath)
	if err != nil {
		return fmt.Errorf("unable to load wallet client key file: %v", err)
	}
	clientKeyCert = append(key, cert...)

	unlockReq := &lnrpc.UnlockWalletRequest{
		WalletPassword:    password,
		DcrwClientKeyCert: clientKeyCert,
	}
	err = hn.Unlock(unlockReq)
	if err != nil {
		return fmt.Errorf("unable to unlock remote wallet: %v", err)
	}
	return nil
}

// WaitUntilStarted waits until the wallet state flips from "WAITING_TO_START".
func (hn *HarnessNode) WaitUntilStarted() error {
	return hn.waitTillServerState(func(s lnrpc.WalletState) bool {
		return s != lnrpc.WalletState_WAITING_TO_START
	})
}

// WaitUntilStateReached waits until the given wallet state (or one of the
// states following it) has been reached.
func (hn *HarnessNode) WaitUntilStateReached(
	desiredState lnrpc.WalletState) error {

	return hn.waitTillServerState(func(s lnrpc.WalletState) bool {
		return s >= desiredState
	})
}

// WaitUntilServerActive waits until the lnd daemon is fully started.
func (hn *HarnessNode) WaitUntilServerActive() error {
	return hn.waitTillServerState(func(s lnrpc.WalletState) bool {
		return s == lnrpc.WalletState_SERVER_ACTIVE
	})
}

// WaitUntilLeader attempts to finish the start procedure by initiating an RPC
// connection and setting up the wallet unlocker client. This is needed when
// a node that has recently been started was waiting to become the leader and
// we're at the point when we expect that it is the leader now (awaiting
// unlock).
func (hn *HarnessNode) WaitUntilLeader(timeout time.Duration) error {
	var (
		conn    *grpc.ClientConn
		connErr error
	)

	if err := wait.NoError(func() error {
		conn, connErr = hn.ConnectRPC(!hn.Cfg.HasSeed)
		return connErr
	}, timeout); err != nil {
		return err
	}

	// Init all the RPC clients.
	hn.initRPCClients(conn)

	// If the node was created with a seed, we will need to perform an
	// additional step to unlock the wallet. The connection returned will
	// only use the TLS certs, and can only perform operations necessary to
	// unlock the daemon.
	if hn.Cfg.HasSeed {
		// TODO(yy): remove
		hn.WalletUnlockerClient = lnrpc.NewWalletUnlockerClient(conn)

		return nil
	}

	return hn.initLightningClient()
}

// initClientWhenReady waits until the main gRPC server is detected as active,
// then complete the normal HarnessNode gRPC connection creation. This can be
// used it a node has just been unlocked, or has its wallet state initialized.
func (hn *HarnessNode) initClientWhenReady(timeout time.Duration) error {
	var (
		conn    *grpc.ClientConn
		connErr error
	)
	if err := wait.NoError(func() error {
		conn, connErr = hn.ConnectRPC(true)
		return connErr
	}, timeout); err != nil {
		return err
	}

	// Init all the RPC clients.
	hn.initRPCClients(conn)

	return hn.initLightningClient()
}

func (hn *HarnessNode) initRemoteWallet(ctx context.Context,
	initReq *lnrpc.InitWalletRequest) (*lnrpc.InitWalletResponse, error) {

	var mnemonic aezeed.Mnemonic
	copy(mnemonic[:], initReq.CipherSeedMnemonic)
	deciphered, err := mnemonic.Decipher(initReq.AezeedPassphrase)
	if err != nil {
		return nil, err
	}
	// The returned HD seed are the last 16 bytes of the deciphered aezeed
	// byte slice.
	seed := deciphered[len(deciphered)-16:]

	loader := pb.NewWalletLoaderServiceClient(hn.walletConn)
	reqCreate := &pb.CreateWalletRequest{
		PrivatePassphrase: initReq.WalletPassword,
		Seed:              seed,
	}
	_, err = loader.CreateWallet(ctx, reqCreate)
	if err != nil {
		return nil, err
	}

	// Set the wallet to use per-account passphrases.
	wallet := pb.NewWalletServiceClient(hn.walletConn)
	reqSetAcctPwd := &pb.SetAccountPassphraseRequest{
		AccountNumber:        0,
		WalletPassphrase:     initReq.WalletPassword,
		NewAccountPassphrase: initReq.WalletPassword,
	}
	_, err = wallet.SetAccountPassphrase(ctx, reqSetAcctPwd)
	if err != nil {
		return nil, err
	}

	err = hn.Cfg.BackendCfg.StartWalletSync(loader, initReq.WalletPassword)
	if err != nil {
		return nil, err
	}
	unlockReq := &lnrpc.UnlockWalletRequest{
		WalletPassword: initReq.WalletPassword,
		ChannelBackups: initReq.ChannelBackups,
		RecoveryWindow: initReq.RecoveryWindow,
		StatelessInit:  initReq.StatelessInit,
	}
	unlockRes, err := hn.UnlockWallet(ctx, unlockReq)
	if err != nil {
		return nil, fmt.Errorf("unable to unlock wallet: %v", err)
	}

	// Convert from UnlockWalletResponse to InitWalletResponse so that
	// the caller may verify the macaroon generation when initializing in
	// stateless mode.
	return &lnrpc.InitWalletResponse{AdminMacaroon: unlockRes.AdminMacaroon}, nil
}

// Init initializes a harness node by passing the init request via rpc. After
// the request is submitted, this method will block until a
// macaroon-authenticated RPC connection can be established to the harness
// node. Once established, the new connection is used to initialize the
// LightningClient and subscribes the HarnessNode to topology changes.
func (hn *HarnessNode) Init(
	initReq *lnrpc.InitWalletRequest) (*lnrpc.InitWalletResponse, error) {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	var response *lnrpc.InitWalletResponse
	var err error
	if hn.Cfg.RemoteWallet {
		response, err = hn.initRemoteWallet(ctxt, initReq)
	} else {
		response, err = hn.InitWallet(ctxt, initReq)
	}

	if err != nil {
		return nil, err
	}

	// Wait for the wallet to finish unlocking, such that we can connect to
	// it via a macaroon-authenticated rpc connection.
	var conn *grpc.ClientConn
	if err = wait.NoError(func() error {
		// If the node has been initialized stateless, we need to pass
		// the macaroon to the client.
		if initReq.StatelessInit {
			adminMac := &macaroon.Macaroon{}
			err := adminMac.UnmarshalBinary(response.AdminMacaroon)
			if err != nil {
				return err
			}
			conn, err = hn.ConnectRPCWithMacaroon(adminMac)
			return err
		}

		// Normal initialization, we expect a macaroon to be in the
		// file system.
		conn, err = hn.ConnectRPC(true)
		return err
	}, DefaultTimeout); err != nil {
		return nil, err
	}

	// Init all the RPC clients.
	hn.initRPCClients(conn)

	return response, hn.initLightningClient()
}

// InitChangePassword initializes a harness node by passing the change password
// request via RPC. After the request is submitted, this method will block until
// a macaroon-authenticated RPC connection can be established to the harness
// node. Once established, the new connection is used to initialize the
// LightningClient and subscribes the HarnessNode to topology changes.
func (hn *HarnessNode) InitChangePassword(
	chngPwReq *lnrpc.ChangePasswordRequest) (*lnrpc.ChangePasswordResponse,
	error) {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()
	response, err := hn.ChangePassword(ctxt, chngPwReq)
	if err != nil {
		return nil, err
	}

	// Wait for the wallet to finish unlocking, such that we can connect to
	// it via a macaroon-authenticated rpc connection.
	var conn *grpc.ClientConn
	if err = wait.Predicate(func() bool {
		// If the node has been initialized stateless, we need to pass
		// the macaroon to the client.
		if chngPwReq.StatelessInit {
			adminMac := &macaroon.Macaroon{}
			err := adminMac.UnmarshalBinary(response.AdminMacaroon)
			if err != nil {
				return false
			}
			conn, err = hn.ConnectRPCWithMacaroon(adminMac)
			return err == nil
		}

		// Normal initialization, we expect a macaroon to be in the
		// file system.
		conn, err = hn.ConnectRPC(true)
		return err == nil
	}, DefaultTimeout); err != nil {
		return nil, err
	}

	// Init all the RPC clients.
	hn.initRPCClients(conn)

	return response, hn.initLightningClient()
}

// Unlock attempts to unlock the wallet of the target HarnessNode. This method
// should be called after the restart of a HarnessNode that was created with a
// seed+password. Once this method returns, the HarnessNode will be ready to
// accept normal gRPC requests and harness command.
func (hn *HarnessNode) Unlock(unlockReq *lnrpc.UnlockWalletRequest) error {
	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	// Otherwise, we'll need to unlock the node before it's able to start
	// up properly.
	_, err := hn.rpc.WalletUnlocker.UnlockWallet(ctxt, unlockReq)
	if err != nil {
		return err
	}

	// Now that the wallet has been unlocked, we'll wait for the RPC client
	// to be ready, then establish the normal gRPC connection.
	return hn.initClientWhenReady(DefaultTimeout)
}

// waitTillServerState makes a subscription to the server's state change and
// blocks until the server is in the targeted state.
func (hn *HarnessNode) waitTillServerState(
	predicate func(state lnrpc.WalletState) bool) error {

	ctxt, cancel := context.WithTimeout(hn.runCtx, NodeStartTimeout)
	defer cancel()

	client, err := hn.rpc.State.SubscribeState(
		ctxt, &lnrpc.SubscribeStateRequest{},
	)
	if err != nil {
		return fmt.Errorf("failed to subscribe to state: %w", err)
	}

	errChan := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		for {
			resp, err := client.Recv()
			if err != nil {
				errChan <- err
				return
			}

			if predicate(resp.State) {
				close(done)
				return
			}
		}
	}()

	var lastErr error
	for {
		select {
		case err := <-errChan:
			lastErr = err

		case <-done:
			return nil

		case <-time.After(NodeStartTimeout):
			return fmt.Errorf("timeout waiting for state, "+
				"got err from stream: %v", lastErr)
		}
	}
}

// initRPCClients initializes a list of RPC clients for the node.
func (hn *HarnessNode) initRPCClients(c *grpc.ClientConn) {
	hn.rpc = &RPCClients{
		conn:             c,
		LN:               lnrpc.NewLightningClient(c),
		Invoice:          invoicesrpc.NewInvoicesClient(c),
		Router:           routerrpc.NewRouterClient(c),
		WalletKit:        walletrpc.NewWalletKitClient(c),
		WalletUnlocker:   lnrpc.NewWalletUnlockerClient(c),
		Watchtower:       watchtowerrpc.NewWatchtowerClient(c),
		WatchtowerClient: wtclientrpc.NewWatchtowerClientClient(c),
		Signer:           signrpc.NewSignerClient(c),
		State:            lnrpc.NewStateClient(c),
	}
}

// initLightningClient blocks until the lnd server is fully started and
// subscribes the harness node to graph topology updates. This method also
// spawns a lightning network watcher for this node, which watches for topology
// changes.
func (hn *HarnessNode) initLightningClient() error {
	// TODO(yy): remove
	// Construct the LightningClient that will allow us to use the
	// HarnessNode directly for normal rpc operations.
	conn := hn.rpc.conn
	hn.LightningClient = lnrpc.NewLightningClient(conn)
	hn.InvoicesClient = invoicesrpc.NewInvoicesClient(conn)
	hn.RouterClient = routerrpc.NewRouterClient(conn)
	hn.WalletKitClient = walletrpc.NewWalletKitClient(conn)
	hn.Watchtower = watchtowerrpc.NewWatchtowerClient(conn)
	hn.WatchtowerClient = wtclientrpc.NewWatchtowerClientClient(conn)
	hn.SignerClient = signrpc.NewSignerClient(conn)
	hn.StateClient = lnrpc.NewStateClient(conn)

	// Wait until the server is fully started.
	if err := hn.WaitUntilServerActive(); err != nil {
		return err
	}

	// Set the harness node's pubkey to what the node claims in GetInfo.
	// The RPC must have been started at this point.
	if err := hn.FetchNodeInfo(); err != nil {
		return err
	}

	err := hn.WaitForBlockchainSync()
	if err != nil {
		return fmt.Errorf("initial blockchain sync of %s failed: %v", hn.Name(),
			err)
	}

	// Wait until the node has started all subservices.
	ctxb := context.Background()
	wait.Predicate(func() bool {
		info, err := hn.GetInfo(ctxb, &lnrpc.GetInfoRequest{})
		if err != nil {
			return false
		}

		return info.ServerActive
	}, 30*time.Second)

	// Launch the watcher that will hook into graph related topology change
	// from the PoV of this node.
	hn.wg.Add(1)
	go hn.lightningNetworkWatcher()

	return nil
}

// FetchNodeInfo queries an unlocked node to retrieve its public key.
func (hn *HarnessNode) FetchNodeInfo() error {
	// Obtain the lnid of this node for quick identification purposes.
	info, err := hn.rpc.LN.GetInfo(hn.runCtx, &lnrpc.GetInfoRequest{})
	if err != nil {
		return err
	}

	hn.PubKeyStr = info.IdentityPubkey

	pubkey, err := hex.DecodeString(info.IdentityPubkey)
	if err != nil {
		return err
	}
	copy(hn.PubKey[:], pubkey)

	return nil
}

// AddToLog adds a line of choice to the node's logfile. This is useful
// to interleave test output with output from the node.
func (hn *HarnessNode) AddToLog(format string, a ...interface{}) {
	// If this node was not set up with a log file, just return early.
	if hn.logFile == nil {
		return
	}

	desc := fmt.Sprintf("itest: %s\n", fmt.Sprintf(format, a...))
	if _, err := hn.logFile.WriteString(desc); err != nil {
		hn.PrintErr("write to log err: %v", err)
	}
}

// ReadMacaroon waits a given duration for the macaroon file to be created. If
// the file is readable within the timeout, its content is de-serialized as a
// macaroon and returned.
func (hn *HarnessNode) ReadMacaroon(macPath string, timeout time.Duration) (
	*macaroon.Macaroon, error) {

	// Wait until macaroon file is created and has valid content before
	// using it.
	var mac *macaroon.Macaroon
	err := wait.NoError(func() error {
		macBytes, err := ioutil.ReadFile(macPath)
		if err != nil {
			return fmt.Errorf("error reading macaroon file: %v", err)
		}

		newMac := &macaroon.Macaroon{}
		if err = newMac.UnmarshalBinary(macBytes); err != nil {
			return fmt.Errorf("error unmarshalling macaroon file: %v", err)
		}
		mac = newMac

		return nil
	}, time.Second*30)

	return mac, err
}

// ConnectRPCWithMacaroon uses the TLS certificate and given macaroon to
// create a gRPC client connection.
func (hn *HarnessNode) ConnectRPCWithMacaroon(mac *macaroon.Macaroon) (
	*grpc.ClientConn, error) {

	// Wait until TLS certificate is created and has valid content before
	// using it, up to 30 sec.
	var tlsCreds credentials.TransportCredentials
	err := wait.NoError(func() error {
		var err error
		tlsCreds, err = credentials.NewClientTLSFromFile(
			hn.Cfg.TLSCertPath, "",
		)
		return err
	}, time.Second*30)
	if err != nil {
		return nil, fmt.Errorf("error reading TLS cert: %v", err)
	}

	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Millisecond * 20,
				Multiplier: 1,
				Jitter:     0.2,
				MaxDelay:   time.Millisecond * 20,
			},
			MinConnectTimeout: time.Millisecond * 20,
		}),
	}

	ctx, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	if mac == nil {
		return grpc.DialContext(ctx, hn.Cfg.RPCAddr(), opts...)
	}
	macCred, err := macaroons.NewMacaroonCredential(mac)
	if err != nil {
		return nil, fmt.Errorf("error cloning mac: %v", err)
	}
	opts = append(opts, grpc.WithPerRPCCredentials(macCred))

	return grpc.DialContext(ctx, hn.Cfg.RPCAddr(), opts...)
}

// ConnectRPC uses the TLS certificate and admin macaroon files written by the
// lnd node to create a gRPC client connection.
func (hn *HarnessNode) ConnectRPC(useMacs bool) (*grpc.ClientConn, error) {
	// If we don't want to use macaroons, just pass nil, the next method
	// will handle it correctly.
	if !useMacs {
		return hn.ConnectRPCWithMacaroon(nil)
	}

	// If we should use a macaroon, always take the admin macaroon as a
	// default.
	mac, err := hn.ReadMacaroon(hn.Cfg.AdminMacPath, DefaultTimeout)
	if err != nil {
		return nil, err
	}
	return hn.ConnectRPCWithMacaroon(mac)
}

// SetExtraArgs assigns the ExtraArgs field for the node's configuration. The
// changes will take effect on restart.
func (hn *HarnessNode) SetExtraArgs(extraArgs []string) {
	hn.Cfg.ExtraArgs = extraArgs
}

// cleanup cleans up all the temporary files created by the node's process.
func (hn *HarnessNode) cleanup() error {
	if hn.backupDbDir != "" {
		err := os.RemoveAll(hn.backupDbDir)
		if err != nil {
			return fmt.Errorf("unable to remove backup dir: %v",
				err)
		}
	}

	return os.RemoveAll(hn.Cfg.BaseDir)
}

// Stop attempts to stop the active lnd process.
func (hn *HarnessNode) stop() error {
	// Do nothing if the process is not running.
	if hn.runCtx == nil {
		return nil
	}

	// If start() failed before creating clients, we will just wait for the
	// child process to die.
	if hn.rpc != nil && hn.rpc.LN != nil {
		// Don't watch for error because sometimes the RPC connection
		// gets closed before a response is returned.
		req := lnrpc.StopRequest{}
		err := wait.NoError(func() error {
			_, err := hn.rpc.LN.StopDaemon(hn.runCtx, &req)
			switch {
			case err == nil:
				return nil

			// Try again if a recovery/rescan is in progress.
			case strings.Contains(
				err.Error(), "recovery in progress",
			):
				return err

			default:
				return nil
			}
		}, DefaultTimeout)
		if err != nil {
			return err
		}
	}

	// Stop the remote dcrwallet instance if running a remote wallet.
	if hn.walletCmd != nil {
		hn.walletCmd.Process.Signal(os.Interrupt)
	}

	// Stop the runCtx and wait for goroutines to finish.
	hn.cancel()

	// Wait for lnd process to exit.
	err := wait.NoError(func() error {
		if hn.cmd.ProcessState == nil {
			return fmt.Errorf("process did not exit")
		}

		if !hn.cmd.ProcessState.Exited() {
			return fmt.Errorf("process did not exit")
		}

		// Wait for goroutines to be finished.
		hn.wg.Wait()

		return nil
	}, DefaultTimeout*2)
	if err != nil {
		return err
	}

	hn.LightningClient = nil
	hn.WalletUnlockerClient = nil
	hn.Watchtower = nil
	hn.WatchtowerClient = nil

	// Close any attempts at further grpc connections.
	if hn.rpc.conn != nil {
		err := status.Code(hn.rpc.conn.Close())
		switch err {
		case codes.OK:
			return nil

		// When the context is canceled above, we might get the
		// following error as the context is no longer active.
		case codes.Canceled:
			return nil

		case codes.Unknown:
			return fmt.Errorf("unknown error attempting to stop "+
				"grpc client: %v", err)

		default:
			return fmt.Errorf("error attempting to stop "+
				"grpc client: %v", err)
		}

	}

	return nil
}

// shutdown stops the active lnd process and cleans up any temporary
// directories created along the way.
func (hn *HarnessNode) shutdown() error {
	if err := hn.stop(); err != nil {
		return err
	}
	if err := hn.cleanup(); err != nil {
		return err
	}
	return nil
}

// kill kills the lnd process
func (hn *HarnessNode) kill() error {
	return hn.cmd.Process.Kill()
}

// P2PAddr returns the configured P2P address for the node.
func (hn *HarnessNode) P2PAddr() string {
	return hn.Cfg.P2PAddr()
}

type chanWatchType uint8

const (
	// watchOpenChannel specifies that this is a request to watch an open
	// channel event.
	watchOpenChannel chanWatchType = iota

	// watchCloseChannel specifies that this is a request to watch a close
	// channel event.
	watchCloseChannel

	// watchPolicyUpdate specifies that this is a request to watch a policy
	// update event.
	watchPolicyUpdate
)

// closeChanWatchRequest is a request to the lightningNetworkWatcher to be
// notified once it's detected within the test Lightning Network, that a
// channel has either been added or closed.
type chanWatchRequest struct {
	chanPoint wire.OutPoint

	chanWatchType chanWatchType

	eventChan chan struct{}

	advertisingNode    string
	policy             *lnrpc.RoutingPolicy
	includeUnannounced bool
}

func (hn *HarnessNode) checkChanPointInGraph(chanPoint wire.OutPoint) bool {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	chanGraph, err := hn.DescribeGraph(ctxt, &lnrpc.ChannelGraphRequest{})
	if err != nil {
		return false
	}

	targetChanPoint := chanPoint.String()
	for _, chanEdge := range chanGraph.Edges {
		candidateChanPoint := chanEdge.ChanPoint
		if targetChanPoint == candidateChanPoint {
			return true
		}
	}

	return false
}

// lightningNetworkWatcher is a goroutine which is able to dispatch
// notifications once it has been observed that a target channel has been
// closed or opened within the network. In order to dispatch these
// notifications, the GraphTopologySubscription client exposed as part of the
// gRPC interface is used.
func (hn *HarnessNode) lightningNetworkWatcher() {
	defer hn.wg.Done()

	graphUpdates := make(chan *lnrpc.GraphTopologyUpdate)

	// Start a goroutine to receive graph updates.
	hn.wg.Add(1)
	go func() {
		defer hn.wg.Done()
		err := hn.receiveTopologyClientStream(graphUpdates)
		if err != nil {
			hn.PrintErr("receive topology client stream "+
				"got err:%v", err)
		}
	}()

	for {
		select {

		// A new graph update has just been received, so we'll examine
		// the current set of registered clients to see if we can
		// dispatch any requests.
		case graphUpdate := <-graphUpdates:
			hn.handleChannelEdgeUpdates(graphUpdate.ChannelUpdates)
			hn.handleClosedChannelUpdate(graphUpdate.ClosedChans)
			// TODO(yy): handle node updates too

		// A new watch request, has just arrived. We'll either be able
		// to dispatch immediately, or need to add the client for
		// processing later.
		case watchRequest := <-hn.chanWatchRequests:
			switch watchRequest.chanWatchType {
			case watchOpenChannel:
				// TODO(roasbeef): add update type also, checks
				// for multiple of 2
				hn.handleOpenChannelWatchRequest(watchRequest)

			case watchCloseChannel:
				hn.handleCloseChannelWatchRequest(watchRequest)

			case watchPolicyUpdate:
				hn.handlePolicyUpdateWatchRequest(watchRequest)
			}

		case <-hn.runCtx.Done():
			return
		}
	}
}

// WaitForNetworkChannelOpen will block until a channel with the target
// outpoint is seen as being fully advertised within the network. A channel is
// considered "fully advertised" once both of its directional edges has been
// advertised within the test Lightning Network.
func (hn *HarnessNode) WaitForNetworkChannelOpen(
	chanPoint *lnrpc.ChannelPoint) error {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	eventChan := make(chan struct{})

	op, err := MakeOutpoint(chanPoint)
	if err != nil {
		return fmt.Errorf("failed to create outpoint for %v "+
			"got err: %v", chanPoint, err)
	}

	hn.LogPrintf("Going to wait for open of %s", op)
	hn.chanWatchRequests <- &chanWatchRequest{
		chanPoint:     op,
		eventChan:     eventChan,
		chanWatchType: watchOpenChannel,
	}

	select {
	case <-eventChan:
		return nil
	case <-ctxt.Done():
		return fmt.Errorf("channel:%s not opened before timeout: %s",
			op, hn)
	}
}

// WaitForNetworkChannelClose will block until a channel with the target
// outpoint is seen as closed within the network. A channel is considered
// closed once a transaction spending the funding outpoint is seen within a
// confirmed block.
func (hn *HarnessNode) WaitForNetworkChannelClose(
	chanPoint *lnrpc.ChannelPoint) error {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	eventChan := make(chan struct{})

	op, err := MakeOutpoint(chanPoint)
	if err != nil {
		return fmt.Errorf("failed to create outpoint for %v "+
			"got err: %v", chanPoint, err)
	}

	hn.chanWatchRequests <- &chanWatchRequest{
		chanPoint:     op,
		eventChan:     eventChan,
		chanWatchType: watchCloseChannel,
	}

	select {
	case <-eventChan:
		return nil
	case <-ctxt.Done():
		return fmt.Errorf("channel:%s not closed before timeout: "+
			"%s", op, hn)
	}
}

// WaitForChannelPolicyUpdate will block until a channel policy with the target
// outpoint and advertisingNode is seen within the network.
func (hn *HarnessNode) WaitForChannelPolicyUpdate(
	advertisingNode string, policy *lnrpc.RoutingPolicy,
	chanPoint *lnrpc.ChannelPoint, includeUnannounced bool) error {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	eventChan := make(chan struct{})

	op, err := MakeOutpoint(chanPoint)
	if err != nil {
		return fmt.Errorf("failed to create outpoint for %v"+
			"got err: %v", chanPoint, err)
	}

	ticker := time.NewTicker(wait.PollInterval)
	defer ticker.Stop()

	for {
		select {
		// Send a watch request every second.
		case <-ticker.C:
			// Did the event can close in the meantime? We want to
			// avoid a "close of closed channel" panic since we're
			// re-using the same event chan for multiple requests.
			select {
			case <-eventChan:
				return nil
			default:
			}

			hn.chanWatchRequests <- &chanWatchRequest{
				chanPoint:          op,
				eventChan:          eventChan,
				chanWatchType:      watchPolicyUpdate,
				policy:             policy,
				advertisingNode:    advertisingNode,
				includeUnannounced: includeUnannounced,
			}

		case <-eventChan:
			return nil

		case <-ctxt.Done():
			return fmt.Errorf("channel:%s policy not updated "+
				"before timeout: [%s:%v] %s", op,
				advertisingNode, policy, hn.String())
		}
	}
}

// WaitForBlockchainSync waits for the target node to be fully synchronized with
// the blockchain. If the passed context object has a set timeout, it will
// continually poll until the timeout has elapsed. In the case that the chain
// isn't synced before the timeout is up, this function will return an error.
func (hn *HarnessNode) WaitForBlockchainSync() error {
	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	ticker := time.NewTicker(time.Millisecond * 100)
	defer ticker.Stop()

	for {
		resp, err := hn.rpc.LN.GetInfo(ctxt, &lnrpc.GetInfoRequest{})
		if err != nil {
			return err
		}
		if resp.SyncedToChain {
			return nil
		}

		select {
		case <-ctxt.Done():
			return fmt.Errorf("timeout while waiting for " +
				"blockchain sync")
		case <-hn.runCtx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// WaitForBlockHeight  will block until the target node syncs to the given
// block height or the context expires.
func (hn *HarnessNode) WaitForBlockHeight(ctx context.Context, height uint32) error {
	errChan := make(chan error, 1)
	retryDelay := time.Millisecond * 100

	go func() {
		for {
			select {
			case <-ctx.Done():
			case <-hn.runCtx.Done():
				return
			default:
			}

			getInfoReq := &lnrpc.GetInfoRequest{}
			getInfoResp, err := hn.GetInfo(ctx, getInfoReq)
			if err != nil {
				errChan <- err
				return
			}
			if getInfoResp.SyncedToChain && getInfoResp.BlockHeight == height {
				errChan <- nil
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(retryDelay):
			}
		}
	}()

	select {
	case <-hn.runCtx.Done():
		return nil
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout while waiting for blockchain sync")
	}
}

// WaitForBalance waits until the node sees the expected confirmed/unconfirmed
// balance within their wallet.
func (hn *HarnessNode) WaitForBalance(expectedBalance dcrutil.Amount,
	confirmed bool) error {

	req := &lnrpc.WalletBalanceRequest{}

	var lastBalance dcrutil.Amount
	doesBalanceMatch := func() bool {
		balance, err := hn.rpc.LN.WalletBalance(hn.runCtx, req)
		if err != nil {
			return false
		}

		if confirmed {
			lastBalance = dcrutil.Amount(balance.ConfirmedBalance)
			return dcrutil.Amount(balance.ConfirmedBalance) == expectedBalance
		}

		lastBalance = dcrutil.Amount(balance.UnconfirmedBalance)
		return dcrutil.Amount(balance.UnconfirmedBalance) == expectedBalance
	}

	err := wait.Predicate(doesBalanceMatch, DefaultTimeout)
	if err != nil {
		return fmt.Errorf("balances not synced after deadline: "+
			"expected %v, only have %v", expectedBalance, lastBalance)
	}

	return nil
}

// PrintErr prints an error to the console.
func (hn *HarnessNode) PrintErr(format string, a ...interface{}) {
	fmt.Printf("itest error from [node:%s]: %s\n",
		hn.Cfg.Name, fmt.Sprintf(format, a...))
}

// handleChannelEdgeUpdates takes a series of channel edge updates, extracts
// the outpoints, and saves them to harness node's internal state.
func (hn *HarnessNode) handleChannelEdgeUpdates(
	updates []*lnrpc.ChannelEdgeUpdate) {

	// For each new channel, we'll increment the number of
	// edges seen by one.
	for _, newChan := range updates {
		op, err := MakeOutpoint(newChan.ChanPoint)
		if err != nil {
			hn.PrintErr("failed to create outpoint for %v "+
				"got err: %v", newChan.ChanPoint, err)
			return
		}
		hn.openChans[op]++

		hn.LogPrintf("Got open chan update for %s (edges %d, watchers %d)",
			op, hn.openChans[op], len(hn.openChanWatchers[op]))

		// For this new channel, if the number of edges seen is less
		// than two, then the channel hasn't been fully announced yet.
		if numEdges := hn.openChans[op]; numEdges < 2 {
			return
		}

		// Otherwise, we'll notify all the registered watchers and
		// remove the dispatched watchers.
		for _, eventChan := range hn.openChanWatchers[op] {
			close(eventChan)
		}
		delete(hn.openChanWatchers, op)

		// Check whether there's a routing policy update. If so, save
		// it to the node state.
		if newChan.RoutingPolicy == nil {
			continue
		}

		// Append the policy to the slice.
		node := newChan.AdvertisingNode
		policies := hn.policyUpdates[op.String()]

		// If the map[op] is nil, we need to initialize the map first.
		if policies == nil {
			policies = make(map[string][]*lnrpc.RoutingPolicy)
		}
		policies[node] = append(
			policies[node], newChan.RoutingPolicy,
		)
		hn.policyUpdates[op.String()] = policies
	}
}

// handleOpenChannelWatchRequest processes a watch open channel request by
// checking the number of the edges seen for a given channel point. If the
// number is no less than 2 then the channel is considered open. Otherwise, we
// will attempt to find it in its channel graph. If neither can be found, the
// request is added to a watch request list than will be handled by
// handleChannelEdgeUpdates.
func (hn *HarnessNode) handleOpenChannelWatchRequest(req *chanWatchRequest) {
	targetChan := req.chanPoint

	// If this is an open request, then it can be dispatched if the number
	// of edges seen for the channel is at least two.
	if numEdges := hn.openChans[targetChan]; numEdges >= 2 {
		hn.LogPrintf("Already have targetChan opened: %s", targetChan)
		close(req.eventChan)
		return
	}

	// Before we add the channel to our set of open clients, we'll check to
	// see if the channel is already in the channel graph of the target
	// node. This lets us handle the case where a node has already seen a
	// channel before a notification has been requested, causing us to miss
	// it.
	chanFound := hn.checkChanPointInGraph(targetChan)
	if chanFound {
		hn.LogPrintf("Already have targetChan in graph: %s", targetChan)
		close(req.eventChan)
		return
	}

	// Otherwise, we'll add this to the list of open channel watchers for
	// this out point.
	hn.openChanWatchers[targetChan] = append(
		hn.openChanWatchers[targetChan],
		req.eventChan,
	)
	hn.LogPrintf("Registered targetChan to wait for open: %s", targetChan)
}

// handleClosedChannelUpdate takes a series of closed channel updates, extracts
// the outpoints, saves them to harness node's internal state, and notifies all
// registered clients.
func (hn *HarnessNode) handleClosedChannelUpdate(
	updates []*lnrpc.ClosedChannelUpdate) {

	// For each channel closed, we'll mark that we've detected a channel
	// closure while lnd was pruning the channel graph.
	for _, closedChan := range updates {
		op, err := MakeOutpoint(closedChan.ChanPoint)
		if err != nil {
			hn.PrintErr("failed to create outpoint for %v "+
				"got err: %v", closedChan.ChanPoint, err)
			return
		}

		hn.closedChans[op] = struct{}{}

		// As the channel has been closed, we'll notify all register
		// watchers.
		for _, eventChan := range hn.closeChanWatchers[op] {
			close(eventChan)
		}
		delete(hn.closeChanWatchers, op)
	}
}

// handleCloseChannelWatchRequest processes a watch close channel request by
// checking whether the given channel point can be found in the node's internal
// state. If not, the request is added to a watch request list than will be
// handled by handleCloseChannelWatchRequest.
func (hn *HarnessNode) handleCloseChannelWatchRequest(req *chanWatchRequest) {
	targetChan := req.chanPoint

	// If this is a close request, then it can be immediately dispatched if
	// we've already seen a channel closure for this channel.
	if _, ok := hn.closedChans[targetChan]; ok {
		close(req.eventChan)
		return
	}

	// Otherwise, we'll add this to the list of close channel watchers for
	// this out point.
	hn.closeChanWatchers[targetChan] = append(
		hn.closeChanWatchers[targetChan],
		req.eventChan,
	)
}

type topologyClient lnrpc.Lightning_SubscribeChannelGraphClient

// newTopologyClient creates a topology client.
func (hn *HarnessNode) newTopologyClient(
	ctx context.Context) (topologyClient, error) {

	req := &lnrpc.GraphTopologySubscription{}
	client, err := hn.rpc.LN.SubscribeChannelGraph(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s(%d): unable to create topology "+
			"client: %v (%s)", hn.Name(), hn.NodeID, err,
			time.Now().String())
	}

	return client, nil
}

// receiveTopologyClientStream initializes a topologyClient to subscribe
// topology update events. Due to a race condition between the ChannelRouter
// starting and us making the subscription request, it's possible for our graph
// subscription to fail. In that case, we will retry the subscription until it
// succeeds or fail after 10 seconds.
//
// NOTE: must be run as a goroutine.
func (hn *HarnessNode) receiveTopologyClientStream(
	receiver chan *lnrpc.GraphTopologyUpdate) error {

	// Create a topology client to receive graph updates.
	client, err := hn.newTopologyClient(hn.runCtx)
	if err != nil {
		return fmt.Errorf("create topologyClient failed: %v", err)
	}

	// We use the context to time out when retrying graph subscription.
	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	for {
		update, err := client.Recv()

		switch {
		case err == nil:
			// Good case. We will send the update to the receiver.

		case strings.Contains(err.Error(), "router not started"):
			// If the router hasn't been started, we will retry
			// every 200 ms until it has been started or fail
			// after the ctxt is timed out.
			select {
			case <-ctxt.Done():
				return fmt.Errorf("graph subscription: " +
					"router not started before timeout")
			case <-time.After(wait.PollInterval):
			case <-hn.runCtx.Done():
				return nil
			}

			// Re-create the topology client.
			client, err = hn.newTopologyClient(hn.runCtx)
			if err != nil {
				return fmt.Errorf("create topologyClient "+
					"failed: %v", err)
			}

			continue

		case strings.Contains(err.Error(), "EOF"):
			// End of subscription stream. Do nothing and quit.
			return nil

		case strings.Contains(err.Error(), context.Canceled.Error()):
			// End of subscription stream. Do nothing and quit.
			return nil

		default:
			// An expected error is returned, return and leave it
			// to be handled by the caller.
			return fmt.Errorf("graph subscription err: %w", err)
		}

		// Send the update or quit.
		select {
		case receiver <- update:
		case <-hn.runCtx.Done():
			return nil
		}
	}
}

// handlePolicyUpdateWatchRequest checks that if the expected policy can be
// found either in the node's interval state or describe graph response. If
// found, it will signal the request by closing the event channel. Otherwise it
// does nothing but returns nil.
func (hn *HarnessNode) handlePolicyUpdateWatchRequest(req *chanWatchRequest) {
	op := req.chanPoint

	// Get a list of known policies for this chanPoint+advertisingNode
	// combination. Start searching in the node state first.
	policies, ok := hn.policyUpdates[op.String()][req.advertisingNode]

	if !ok {
		// If it cannot be found in the node state, try searching it
		// from the node's DescribeGraph.
		policyMap := hn.getChannelPolicies(req.includeUnannounced)
		policies, ok = policyMap[op.String()][req.advertisingNode]
		if !ok {
			return
		}
	}

	// Check if there's a matched policy.
	for _, policy := range policies {
		if CheckChannelPolicy(policy, req.policy) == nil {
			close(req.eventChan)
			return
		}
	}
}

// getChannelPolicies queries the channel graph and formats the policies into
// the format defined in type policyUpdateMap.
func (hn *HarnessNode) getChannelPolicies(include bool) policyUpdateMap {

	ctxt, cancel := context.WithTimeout(hn.runCtx, DefaultTimeout)
	defer cancel()

	graph, err := hn.rpc.LN.DescribeGraph(ctxt, &lnrpc.ChannelGraphRequest{
		IncludeUnannounced: include,
	})
	if err != nil {
		hn.PrintErr("DescribeGraph got err: %v", err)
		return nil
	}

	policyUpdates := policyUpdateMap{}

	for _, e := range graph.Edges {

		policies := policyUpdates[e.ChanPoint]

		// If the map[op] is nil, we need to initialize the map first.
		if policies == nil {
			policies = make(map[string][]*lnrpc.RoutingPolicy)
		}

		if e.Node1Policy != nil {
			policies[e.Node1Pub] = append(
				policies[e.Node1Pub], e.Node1Policy,
			)
		}

		if e.Node2Policy != nil {
			policies[e.Node2Pub] = append(
				policies[e.Node2Pub], e.Node2Policy,
			)
		}

		policyUpdates[e.ChanPoint] = policies
	}

	return policyUpdates
}

// renameFile is a helper to rename (log) files created during integration
// tests.
func renameFile(fromFileName, toFileName string) {
	err := os.Rename(fromFileName, toFileName)
	if err != nil {
		fmt.Printf("could not rename %s to %s: %v\n",
			fromFileName, toFileName, err)
	}
}

// getFinalizedLogFilePrefix returns the finalize log filename.
func getFinalizedLogFilePrefix(hn *HarnessNode) string {
	pubKeyHex := hex.EncodeToString(
		hn.PubKey[:logPubKeyBytes],
	)

	return fmt.Sprintf("%s/%0.3d-%s-%s-%s",
		GetLogDir(), hn.NodeID, hn.Cfg.Name,
		hn.Cfg.LogFilenamePrefix,
		pubKeyHex)
}

// finalizeLogfile makes sure the log file cleanup function is initialized,
// even if no log file is created.
func finalizeLogfile(hn *HarnessNode, fileName string) {
	if hn.logFile != nil {
		hn.logFile.Close()

		// If logoutput flag is not set, return early.
		if !*logOutput {
			return
		}

		newFileName := fmt.Sprintf("%v.log",
			getFinalizedLogFilePrefix(hn),
		)

		renameFile(fileName, newFileName)
	}
}

func finalizeEtcdLog(hn *HarnessNode) {
	if hn.Cfg.DbBackend != BackendEtcd {
		return
	}

	etcdLogFileName := fmt.Sprintf("%s/etcd.log", hn.Cfg.LogDir)
	newEtcdLogFileName := fmt.Sprintf("%v-etcd.log",
		getFinalizedLogFilePrefix(hn),
	)

	renameFile(etcdLogFileName, newEtcdLogFileName)
}

func addLogFile(hn *HarnessNode) (string, error) {
	var fileName string

	dir := GetLogDir()
	fileName = fmt.Sprintf("%s/%0.3d-%s-%s-%s.log", dir, hn.NodeID,
		hn.Cfg.Name, hn.Cfg.LogFilenamePrefix,
		hex.EncodeToString(hn.PubKey[:logPubKeyBytes]))

	// If the node's PubKey is not yet initialized, create a
	// temporary file name. Later, after the PubKey has been
	// initialized, the file can be moved to its final name with
	// the PubKey included.
	if bytes.Equal(hn.PubKey[:4], []byte{0, 0, 0, 0}) {
		fileName = fmt.Sprintf("%s/%0.3d-%s-%s-tmp__.log", dir,
			hn.NodeID, hn.Cfg.Name, hn.Cfg.LogFilenamePrefix)
	}

	// Create file if not exists, otherwise append.
	file, err := os.OpenFile(fileName,
		os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		return fileName, err
	}

	// Pass node's stderr to both errb and the file.
	w := io.MultiWriter(hn.cmd.Stderr, file)
	hn.cmd.Stderr = w

	// Pass the node's stdout only to the file.
	hn.cmd.Stdout = file

	// Let the node keep a reference to this file, such
	// that we can add to it if necessary.
	hn.logFile = file

	return fileName, nil
}
