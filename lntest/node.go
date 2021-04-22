package lntest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "decred.org/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v3"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/aezeed"
	"github.com/decred/dcrlnd/chanbackup"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/invoicesrpc"
	"github.com/decred/dcrlnd/lnrpc/routerrpc"
	"github.com/decred/dcrlnd/lnrpc/walletrpc"
	"github.com/decred/dcrlnd/lnrpc/watchtowerrpc"
	"github.com/decred/dcrlnd/lnrpc/wtclientrpc"
	"github.com/decred/dcrlnd/lntest/wait"
	"github.com/decred/dcrlnd/macaroons"
	"github.com/go-errors/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

const (
	// defaultNodePort is the start of the range for listening ports of
	// harness nodes. Ports are monotonically increasing starting from this
	// number and are determined by the results of nextAvailablePort().
	defaultNodePort = 29180

	// logPubKeyBytes is the number of bytes of the node's PubKey that will
	// be appended to the log file name. The whole PubKey is too long and
	// not really necessary to quickly identify what node produced which
	// log file.
	logPubKeyBytes = 4

	// trickleDelay is the amount of time in milliseconds between each
	// release of announcements by AuthenticatedGossiper to the network.
	trickleDelay = 50
)

var (
	// numActiveNodes is the number of active nodes within the test network.
	numActiveNodes uint32 = 0

	// lastPort is the last port determined to be free for use by a new
	// node. It should be used atomically.
	lastPort uint32 = defaultNodePort

	// logOutput is a flag that can be set to append the output from the
	// seed nodes to log files.
	logOutput = flag.Bool("logoutput", false,
		"log output from node n to file output-n.log")

	// goroutineDump is a flag that can be set to dump the active
	// goroutines of test nodes on failure.
	goroutineDump = flag.Bool("goroutinedump", false,
		"write goroutine dump from node n to file pprof-n.log")
)

// nextAvailablePort returns the first port that is available for listening by
// a new node. It panics if no port is found and the maximum available TCP port
// is reached.
func nextAvailablePort() int {
	port := atomic.AddUint32(&lastPort, 1)
	for port < 65535 {
		// If there are no errors while attempting to listen on this
		// port, close the socket and return it as available. While it
		// could be the case that some other process picks up this port
		// between the time the socket is closed and it's reopened in
		// the harness node, in practice in CI servers this seems much
		// less likely than simply some other process already being
		// bound at the start of the tests.
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		l, err := net.Listen("tcp4", addr)
		if err == nil {
			err := l.Close()
			if err == nil {
				return int(port)
			}
			return int(port)
		}
		port = atomic.AddUint32(&lastPort, 1)
	}

	// No ports available? Must be a mistake.
	panic("no ports available for listening")
}

// generateListeningPorts returns five ints representing ports to listen on
// designated for the current lightning network test. This returns the next
// available ports for the p2p, rpc, rest, profiling and wallet services.
func generateListeningPorts() (int, int, int, int, int) {
	p2p := nextAvailablePort()
	rpc := nextAvailablePort()
	rest := nextAvailablePort()
	profile := nextAvailablePort()
	wallet := nextAvailablePort()

	return p2p, rpc, rest, profile, wallet
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
	Name       string
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
}

func (cfg NodeConfig) P2PAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.P2PPort))
}

func (cfg NodeConfig) RPCAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.RPCPort))
}

func (cfg NodeConfig) RESTAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.RESTPort))
}

func (cfg NodeConfig) DBPath() string {
	return filepath.Join(cfg.DataDir, "graph",
		fmt.Sprintf("%v/channel.db", cfg.NetParams.Name))
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
	args = append(args, "--nobootstrap")
	args = append(args, "--debuglevel=debug")
	args = append(args, "--defaultchanconfs=1")
	args = append(args, fmt.Sprintf("--defaultremotedelay=%v", DefaultCSV))
	args = append(args, fmt.Sprintf("--rpclisten=%v", cfg.RPCAddr()))
	args = append(args, fmt.Sprintf("--restlisten=%v", cfg.RESTAddr()))
	args = append(args, fmt.Sprintf("--restcors=https://%v", cfg.RESTAddr()))
	args = append(args, fmt.Sprintf("--listen=%v", cfg.P2PAddr()))
	args = append(args, fmt.Sprintf("--externalip=%v", cfg.P2PAddr()))
	args = append(args, fmt.Sprintf("--logdir=%v", cfg.LogDir))
	args = append(args, fmt.Sprintf("--datadir=%v", cfg.DataDir))
	args = append(args, fmt.Sprintf("--tlscertpath=%v", cfg.TLSCertPath))
	args = append(args, fmt.Sprintf("--tlskeypath=%v", cfg.TLSKeyPath))
	args = append(args, fmt.Sprintf("--configfile=%v", cfg.DataDir))
	args = append(args, fmt.Sprintf("--adminmacaroonpath=%v", cfg.AdminMacPath))
	args = append(args, fmt.Sprintf("--readonlymacaroonpath=%v", cfg.ReadMacPath))
	args = append(args, fmt.Sprintf("--invoicemacaroonpath=%v", cfg.InvoiceMacPath))
	args = append(args, fmt.Sprintf("--trickledelay=%v", trickleDelay))
	args = append(args, fmt.Sprintf("--profile=%d", cfg.ProfilePort))

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

	cmd     *exec.Cmd
	pidFile string
	logFile *os.File

	// processExit is a channel that's closed once it's detected that the
	// process this instance of HarnessNode is bound to has exited.
	processExit chan struct{}

	chanWatchRequests chan *chanWatchRequest

	// For each outpoint, we'll track an integer which denotes the number of
	// edges seen for that channel within the network. When this number
	// reaches 2, then it means that both edge advertisements has propagated
	// through the network.
	openChans   map[wire.OutPoint]int
	openClients map[wire.OutPoint][]chan struct{}

	closedChans  map[wire.OutPoint]struct{}
	closeClients map[wire.OutPoint][]chan struct{}

	quit chan struct{}
	wg   sync.WaitGroup

	lnrpc.LightningClient

	lnrpc.WalletUnlockerClient

	invoicesrpc.InvoicesClient

	// conn is the underlying connection to the grpc endpoint of the node.
	conn *grpc.ClientConn

	// RouterClient, WalletKitClient, WatchtowerClient cannot be embedded,
	// because a name collision would occur with LightningClient.
	RouterClient     routerrpc.RouterClient
	WalletKitClient  walletrpc.WalletKitClient
	Watchtower       watchtowerrpc.WatchtowerClient
	WatchtowerClient wtclientrpc.WatchtowerClientClient
}

// Assert *HarnessNode implements the lnrpc.LightningClient interface.
var _ lnrpc.LightningClient = (*HarnessNode)(nil)
var _ lnrpc.WalletUnlockerClient = (*HarnessNode)(nil)
var _ invoicesrpc.InvoicesClient = (*HarnessNode)(nil)

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

	nodeNum := atomic.AddUint32(&numActiveNodes, 1)

	cfg.P2PPort, cfg.RPCPort, cfg.RESTPort, cfg.ProfilePort, cfg.WalletPort = generateListeningPorts()

	err := os.MkdirAll(cfg.DataDir, os.FileMode(0755))
	if err != nil {
		return nil, err
	}

	// Run all tests with accept keysend. The keysend code is very isolated
	// and it is highly unlikely that it would affect regular itests when
	// enabled.
	cfg.AcceptKeySend = true

	return &HarnessNode{
		Cfg:               &cfg,
		NodeID:            int(nodeNum),
		chanWatchRequests: make(chan *chanWatchRequest),
		openChans:         make(map[wire.OutPoint]int),
		openClients:       make(map[wire.OutPoint][]chan struct{}),

		closedChans:  make(map[wire.OutPoint]struct{}),
		closeClients: make(map[wire.OutPoint][]chan struct{}),
	}, nil
}

// DBPath returns the filepath to the channeldb database file for this node.
func (hn *HarnessNode) DBPath() string {
	return hn.Cfg.DBPath()
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

// ChanBackupPath returns the fielpath to the on-disk channels.backup file for
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

// Start launches a new process running lnd. Additionally, the PID of the
// launched process is saved in order to possibly kill the process forcibly
// later.
//
// This may not clean up properly if an error is returned, so the caller should
// call shutdown() regardless of the return value.
func (hn *HarnessNode) start(lndBinary string, lndError chan<- error) error {
	hn.quit = make(chan struct{})

	args := hn.Cfg.genArgs()
	hn.cmd = exec.Command(lndBinary, args...)

	// Redirect stderr output to buffer
	var errb bytes.Buffer
	hn.cmd.Stderr = &errb

	// Make sure the log file cleanup function is initialized, even
	// if no log file is created.
	var finalizeLogfile = func() {
		if hn.logFile != nil {
			hn.logFile.Close()
		}
	}

	// If the logoutput flag is passed, redirect output from the nodes to
	// log files.
	if *logOutput {
		fileName := fmt.Sprintf("output-%.2d-%s-%s.log", hn.NodeID,
			hn.Cfg.Name, hex.EncodeToString(hn.PubKey[:logPubKeyBytes]))

		// If the node's PubKey is not yet initialized, create a temporary
		// file name. Later, after the PubKey has been initialized, the
		// file can be moved to its final name with the PubKey included.
		if bytes.Equal(hn.PubKey[:4], []byte{0, 0, 0, 0}) {
			fileName = fmt.Sprintf("output-%.2d-%s-tmp__.log", hn.NodeID,
				hn.Cfg.Name)

			// Once the node has done its work, the log file can be renamed.
			finalizeLogfile = func() {
				if hn.logFile != nil {
					hn.logFile.Close()

					newFileName := fmt.Sprintf("output-%.2d-%s-%s.log",
						hn.NodeID, hn.Cfg.Name,
						hex.EncodeToString(hn.PubKey[:logPubKeyBytes]))
					err := os.Rename(fileName, newFileName)
					if err != nil {
						fmt.Printf("could not rename "+
							"%s to %s: %v\n",
							fileName, newFileName,
							err)
					}
				}
			}
		}

		// Create file if not exists, otherwise append.
		file, err := os.OpenFile(fileName,
			os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
		if err != nil {
			return err
		}

		// Pass node's stderr to both errb and the file.
		w := io.MultiWriter(&errb, file)
		hn.cmd.Stderr = w

		// Pass the node's stdout only to the file.
		hn.cmd.Stdout = file

		// Let the node keep a reference to this file, such
		// that we can add to it if necessary.
		hn.logFile = file
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
	hn.processExit = make(chan struct{})
	hn.wg.Add(1)
	go func() {
		defer hn.wg.Done()

		err := hn.cmd.Wait()
		if err != nil {
			lndError <- errors.Errorf("%v\n%v\n", err, errb.String())
		}

		if hn.walletCmd != nil {
			err = hn.walletCmd.Wait()
			if err != nil {
				lndError <- errors.Errorf("wallet error during final wait: %v", err)
			}
		}

		// Signal any onlookers that this process has exited.
		close(hn.processExit)

		// Make sure log file is closed and renamed if necessary.
		finalizeLogfile()
	}()

	// Write process ID to a file.
	if err := hn.writePidFile(); err != nil {
		hn.cmd.Process.Kill()
		if hn.walletCmd != nil {
			hn.walletCmd.Process.Kill()
		}
		return err
	}

	// Since Stop uses the LightningClient to stop the node, if we fail to
	// get a connected client, we have to kill the process.
	useMacaroons := !hn.Cfg.HasSeed && !hn.Cfg.RemoteWallet
	conn, err := hn.ConnectRPC(useMacaroons)
	if err != nil {
		hn.cmd.Process.Kill()
		if hn.walletCmd != nil {
			hn.walletCmd.Process.Kill()
		}
		return fmt.Errorf("unable to connect to %s's RPC: %v", hn.Name(), err)
	}

	// If the node was created with a seed, we will need to perform an
	// additional step to unlock the wallet. The connection returned will
	// only use the TLS certs, and can only perform operations necessary to
	// unlock the daemon.
	if hn.Cfg.HasSeed {
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

	return hn.initLightningClient(conn)
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

		err = hn.Cfg.BackendCfg.StartWalletSync(loader, password)
		if err != nil {
			return err
		}
	}

	return nil
}

func (hn *HarnessNode) unlockRemoteWallet() error {
	ctxb := context.Background()
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
	err = hn.Unlock(ctxb, unlockReq)
	if err != nil {
		return fmt.Errorf("unable to unlock remote wallet: %v", err)
	}
	return err
}

// initClientWhenReady waits until the main gRPC server is detected as active,
// then complete the normal HarnessNode gRPC connection creation. This can be
// used it a node has just been unlocked, or has its wallet state initialized.
func (hn *HarnessNode) initClientWhenReady() error {
	var (
		conn    *grpc.ClientConn
		connErr error
	)
	if err := wait.NoError(func() error {
		conn, connErr = hn.ConnectRPC(true)
		if connErr != nil {
			return connErr
		}

		// Ensure we've connected to the gRPC server that has the
		// lightning service (vs the one with the WalletUnlocker
		// service).
		lnClient := lnrpc.NewLightningClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, infoErr := lnClient.GetInfo(ctx, &lnrpc.GetInfoRequest{})
		return infoErr
	}, 5*time.Second); err != nil {
		return err
	}

	return hn.initLightningClient(conn)
}

func (hn *HarnessNode) initRemoteWallet(ctx context.Context,
	initReq *lnrpc.InitWalletRequest) error {

	var mnemonic aezeed.Mnemonic
	copy(mnemonic[:], initReq.CipherSeedMnemonic)
	deciphered, err := mnemonic.Decipher(initReq.AezeedPassphrase)
	if err != nil {
		return err
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
		return err
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
		return err
	}

	err = hn.Cfg.BackendCfg.StartWalletSync(loader, initReq.WalletPassword)
	if err != nil {
		return err
	}
	unlockReq := &lnrpc.UnlockWalletRequest{
		WalletPassword: initReq.WalletPassword,
		ChannelBackups: initReq.ChannelBackups,
		RecoveryWindow: initReq.RecoveryWindow,
	}
	return hn.Unlock(ctx, unlockReq)
}

// Init initializes a harness node by passing the init request via rpc. After
// the request is submitted, this method will block until an
// macaroon-authenticated rpc connection can be established to the harness node.
// Once established, the new connection is used to initialize the
// LightningClient and subscribes the HarnessNode to topology changes.
func (hn *HarnessNode) Init(ctx context.Context,
	initReq *lnrpc.InitWalletRequest) error {

	if hn.Cfg.RemoteWallet {
		return hn.initRemoteWallet(ctx, initReq)
	}

	ctxt, _ := context.WithTimeout(ctx, DefaultTimeout)
	_, err := hn.InitWallet(ctxt, initReq)
	if err != nil {
		return err
	}

	// Wait for the wallet to finish unlocking, such that we can connect to
	// it via a macaroon-authenticated rpc connection.
	return hn.initClientWhenReady()
}

// Unlock attempts to unlock the wallet of the target HarnessNode. This method
// should be called after the restart of a HarnessNode that was created with a
// seed+password. Once this method returns, the HarnessNode will be ready to
// accept normal gRPC requests and harness command.
func (hn *HarnessNode) Unlock(ctx context.Context,
	unlockReq *lnrpc.UnlockWalletRequest) error {

	ctxt, _ := context.WithTimeout(ctx, DefaultTimeout)

	// Otherwise, we'll need to unlock the node before it's able to start
	// up properly.
	if _, err := hn.UnlockWallet(ctxt, unlockReq); err != nil {
		return fmt.Errorf("unable to unlock wallet: %v", err)
	}

	// Now that the wallet has been unlocked, we'll wait for the RPC client
	// to be ready, then establish the normal gRPC connection.
	return hn.initClientWhenReady()
}

// initLightningClient constructs the grpc LightningClient from the given client
// connection and subscribes the harness node to graph topology updates.
// This method also spawns a lightning network watcher for this node,
// which watches for topology changes.
func (hn *HarnessNode) initLightningClient(conn *grpc.ClientConn) error {
	// Construct the LightningClient that will allow us to use the
	// HarnessNode directly for normal rpc operations.
	hn.conn = conn
	hn.LightningClient = lnrpc.NewLightningClient(conn)
	hn.InvoicesClient = invoicesrpc.NewInvoicesClient(conn)
	hn.RouterClient = routerrpc.NewRouterClient(conn)
	hn.WalletKitClient = walletrpc.NewWalletKitClient(conn)
	hn.Watchtower = watchtowerrpc.NewWatchtowerClient(conn)
	hn.WatchtowerClient = wtclientrpc.NewWatchtowerClientClient(conn)

	// Set the harness node's pubkey to what the node claims in GetInfo.
	err := hn.FetchNodeInfo()
	if err != nil {
		return fmt.Errorf("unable to fetch %s's node info: %v", hn.Name(), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = hn.WaitForBlockchainSync(ctx)
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
	subscribed := make(chan error)
	go hn.lightningNetworkWatcher(subscribed)

	return <-subscribed
}

// FetchNodeInfo queries an unlocked node to retrieve its public key.
func (hn *HarnessNode) FetchNodeInfo() error {
	// Obtain the lnid of this node for quick identification purposes.
	ctxb := context.Background()
	info, err := hn.GetInfo(ctxb, &lnrpc.GetInfoRequest{})
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
func (hn *HarnessNode) AddToLog(line string) error {
	// If this node was not set up with a log file, just return early.
	if hn.logFile == nil {
		return nil
	}
	if _, err := hn.logFile.WriteString(line); err != nil {
		return err
	}
	return nil
}

func (hn *HarnessNode) LogPrintf(format string, args ...interface{}) error {
	now := time.Now().Format("2006-01-02 15:04:05.999")
	f := now + " ----------: " + format + "\n"
	return hn.AddToLog(fmt.Sprintf(f, args...))
}

// writePidFile writes the process ID of the running lnd process to a .pid file.
func (hn *HarnessNode) writePidFile() error {
	filePath := filepath.Join(hn.Cfg.BaseDir, fmt.Sprintf("%v.pid", hn.NodeID))

	pid, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer pid.Close()

	_, err = fmt.Fprintf(pid, "%v\n", hn.cmd.Process.Pid)
	if err != nil {
		return err
	}

	hn.pidFile = filePath
	return nil
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

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	if mac == nil {
		return grpc.DialContext(ctx, hn.Cfg.RPCAddr(), opts...)
	}
	macCred := macaroons.NewMacaroonCredential(mac)
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
	return os.RemoveAll(hn.Cfg.BaseDir)
}

// Stop attempts to stop the active lnd process.
func (hn *HarnessNode) stop() error {
	// Do nothing if the process is not running.
	if hn.processExit == nil {
		return nil
	}

	// If start() failed before creating a client, we will just wait for the
	// child process to die.
	if hn.LightningClient != nil {
		// Don't watch for error because sometimes the RPC connection gets
		// closed before a response is returned.
		req := lnrpc.StopRequest{}
		ctx := context.Background()
		hn.LightningClient.StopDaemon(ctx, &req)
	}

	if hn.walletCmd != nil {
		hn.walletCmd.Process.Signal(os.Interrupt)
	}

	// Wait for lnd process and other goroutines to exit.
	select {
	case <-hn.processExit:
	case <-time.After(60 * time.Second):
		return fmt.Errorf("process did not exit")
	}

	close(hn.quit)
	hn.wg.Wait()

	hn.quit = nil
	hn.processExit = nil
	hn.LightningClient = nil
	hn.WalletUnlockerClient = nil
	hn.Watchtower = nil
	hn.WatchtowerClient = nil

	// Close any attempts at further grpc connections.
	if hn.conn != nil {
		err := hn.conn.Close()
		if err != nil {
			return fmt.Errorf("error attempting to stop grpc client: %v", err)
		}
	}

	return nil
}

// shutdown stops the active lnd process and cleans up any temporary directories
// created along the way.
func (hn *HarnessNode) shutdown() error {
	if err := hn.stop(); err != nil {
		return err
	}
	if err := hn.cleanup(); err != nil {
		return err
	}
	return nil
}

// P2PAddr returns the configured P2P address for the node.
func (hn *HarnessNode) P2PAddr() string {
	return hn.Cfg.P2PAddr()
}

// closeChanWatchRequest is a request to the lightningNetworkWatcher to be
// notified once it's detected within the test Lightning Network, that a
// channel has either been added or closed.
type chanWatchRequest struct {
	chanPoint wire.OutPoint

	chanOpen bool

	eventChan chan struct{}
}

// getChanPointFundingTxid returns the given channel point's funding txid in
// raw bytes.
func getChanPointFundingTxid(chanPoint *lnrpc.ChannelPoint) ([]byte, error) {
	var txid []byte

	// A channel point's funding txid can be get/set as a byte slice or a
	// string. In the case it is a string, decode it.
	switch chanPoint.GetFundingTxid().(type) {
	case *lnrpc.ChannelPoint_FundingTxidBytes:
		txid = chanPoint.GetFundingTxidBytes()
	case *lnrpc.ChannelPoint_FundingTxidStr:
		s := chanPoint.GetFundingTxidStr()
		h, err := chainhash.NewHashFromStr(s)
		if err != nil {
			return nil, err
		}

		txid = h[:]
	}

	return txid, nil
}

// lightningNetworkWatcher is a goroutine which is able to dispatch
// notifications once it has been observed that a target channel has been
// closed or opened within the network. In order to dispatch these
// notifications, the GraphTopologySubscription client exposed as part of the
// gRPC interface is used.
func (hn *HarnessNode) lightningNetworkWatcher(subscribed chan error) {
	defer hn.wg.Done()

	graphUpdates := make(chan *lnrpc.GraphTopologyUpdate)
	hn.wg.Add(1)
	go func() {
		defer hn.wg.Done()

		req := &lnrpc.GraphTopologySubscription{}
		ctx, cancelFunc := context.WithCancel(context.Background())
		topologyClient, err := hn.SubscribeChannelGraph(ctx, req)
		if err != nil {
			msg := fmt.Sprintf("%s(%d): unable to create topology "+
				"client: %v (%s)", hn.Name(), hn.NodeID, err,
				time.Now().String())
			hn.LogPrintf(msg)
			subscribed <- fmt.Errorf(msg)
			return
		}
		close(subscribed)

		defer cancelFunc()

		for {
			update, err := topologyClient.Recv()
			if err == io.EOF {
				return
			} else if err != nil {
				hn.LogPrintf("main topology client error: %v", err)
				return
			}

			select {
			case graphUpdates <- update:
			case <-hn.quit:
				return
			}
		}
	}()

	for {
		select {

		// A new graph update has just been received, so we'll examine
		// the current set of registered clients to see if we can
		// dispatch any requests.
		case graphUpdate := <-graphUpdates:
			// For each new channel, we'll increment the number of
			// edges seen by one.
			for _, newChan := range graphUpdate.ChannelUpdates {
				txidHash, _ := getChanPointFundingTxid(newChan.ChanPoint)
				txid, _ := chainhash.NewHash(txidHash)
				op := wire.OutPoint{
					Hash:  *txid,
					Index: newChan.ChanPoint.OutputIndex,
				}
				hn.openChans[op]++

				// For this new channel, if the number of edges
				// seen is less than two, then the channel
				// hasn't been fully announced yet.
				if numEdges := hn.openChans[op]; numEdges < 2 {
					hn.LogPrintf("Delaying openchan edges=%d %s",
						numEdges, op)
					continue
				}

				hn.LogPrintf("Alerting of openchan clients=%d %s",
					len(hn.openClients[op]), op)

				// Otherwise, we'll notify all the registered
				// clients and remove the dispatched clients.
				for _, eventChan := range hn.openClients[op] {
					close(eventChan)
				}
				delete(hn.openClients, op)
			}

			// For each channel closed, we'll mark that we've
			// detected a channel closure while lnd was pruning the
			// channel graph.
			for _, closedChan := range graphUpdate.ClosedChans {
				txidHash, _ := getChanPointFundingTxid(closedChan.ChanPoint)
				txid, _ := chainhash.NewHash(txidHash)
				op := wire.OutPoint{
					Hash:  *txid,
					Index: closedChan.ChanPoint.OutputIndex,
				}
				hn.closedChans[op] = struct{}{}

				hn.LogPrintf("Alerting closedChan clients=%d %s",
					len(hn.closeClients[op]), op)

				// As the channel has been closed, we'll notify
				// all register clients.
				for _, eventChan := range hn.closeClients[op] {
					close(eventChan)
				}
				delete(hn.closeClients, op)
			}

		// A new watch request, has just arrived. We'll either be able
		// to dispatch immediately, or need to add the client for
		// processing later.
		case watchRequest := <-hn.chanWatchRequests:
			targetChan := watchRequest.chanPoint

			// TODO(roasbeef): add update type also, checks for
			// multiple of 2
			if watchRequest.chanOpen {
				// If this is an open request, then it can be
				// dispatched if the number of edges seen for
				// the channel is at least two.
				if numEdges := hn.openChans[targetChan]; numEdges >= 2 {
					hn.LogPrintf("Alerting already open chan %s",
						targetChan)
					close(watchRequest.eventChan)
					continue
				}
				hn.LogPrintf("Going to wait for %s",
					targetChan)

				// Otherwise, we'll add this to the list of
				// watch open clients for this out point.
				hn.openClients[targetChan] = append(
					hn.openClients[targetChan],
					watchRequest.eventChan,
				)
				continue
			}

			// If this is a close request, then it can be
			// immediately dispatched if we've already seen a
			// channel closure for this channel.
			if _, ok := hn.closedChans[targetChan]; ok {
				hn.LogPrintf("Alerting already closed %s",
					targetChan)
				close(watchRequest.eventChan)
				continue
			}
			hn.LogPrintf("Going to wait for close %s", targetChan)

			// Otherwise, we'll add this to the list of close watch
			// clients for this out point.
			hn.closeClients[targetChan] = append(
				hn.closeClients[targetChan],
				watchRequest.eventChan,
			)

		case <-hn.quit:
			return
		}
	}
}

// WaitForNetworkChannelOpen will block until a channel with the target
// outpoint is seen as being fully advertised within the network. A channel is
// considered "fully advertised" once both of its directional edges has been
// advertised within the test Lightning Network.
func (hn *HarnessNode) WaitForNetworkChannelOpen(ctx context.Context,
	op *lnrpc.ChannelPoint) error {

	eventChan := make(chan struct{})

	txidHash, err := getChanPointFundingTxid(op)
	if err != nil {
		return err
	}
	txid, err := chainhash.NewHash(txidHash)
	if err != nil {
		return err
	}

	hn.LogPrintf(fmt.Sprintf("Going to wait for open of %s:%d", txid, op.OutputIndex))
	hn.chanWatchRequests <- &chanWatchRequest{
		chanPoint: wire.OutPoint{
			Hash:  *txid,
			Index: op.OutputIndex,
		},
		eventChan: eventChan,
		chanOpen:  true,
	}

	select {
	case <-eventChan:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("channel not opened before timeout")
	}
}

// WaitForNetworkChannelClose will block until a channel with the target
// outpoint is seen as closed within the network. A channel is considered
// closed once a transaction spending the funding outpoint is seen within a
// confirmed block.
func (hn *HarnessNode) WaitForNetworkChannelClose(ctx context.Context,
	op *lnrpc.ChannelPoint) error {

	eventChan := make(chan struct{})

	txidHash, err := getChanPointFundingTxid(op)
	if err != nil {
		return err
	}
	txid, err := chainhash.NewHash(txidHash)
	if err != nil {
		return err
	}

	hn.chanWatchRequests <- &chanWatchRequest{
		chanPoint: wire.OutPoint{
			Hash:  *txid,
			Index: op.OutputIndex,
		},
		eventChan: eventChan,
		chanOpen:  false,
	}

	select {
	case <-eventChan:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("channel not closed before timeout")
	}
}

// WaitForBlockchainSync will block until the target nodes has fully
// synchronized with the blockchain. If the passed context object has a set
// timeout, then the goroutine will continually poll until the timeout has
// elapsed. In the case that the chain isn't synced before the timeout is up,
// then this function will return an error.
func (hn *HarnessNode) WaitForBlockchainSync(ctx context.Context) error {
	errChan := make(chan error, 1)
	retryDelay := time.Millisecond * 100

	go func() {
		for {
			select {
			case <-ctx.Done():
			case <-hn.quit:
				return
			default:
			}

			getInfoReq := &lnrpc.GetInfoRequest{}
			getInfoResp, err := hn.GetInfo(ctx, getInfoReq)
			if err != nil {
				errChan <- err
				return
			}
			if getInfoResp.SyncedToChain {
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
	case <-hn.quit:
		return nil
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout while waiting for blockchain sync")
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
			case <-hn.quit:
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
	case <-hn.quit:
		return nil
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout while waiting for blockchain sync")
	}
}

// WaitForBalance waits until the node sees the expected confirmed/unconfirmed
// balance within their wallet.
func (hn *HarnessNode) WaitForBalance(expectedBalance dcrutil.Amount, confirmed bool) error {
	ctx := context.Background()
	req := &lnrpc.WalletBalanceRequest{}

	var lastBalance dcrutil.Amount
	doesBalanceMatch := func() bool {
		balance, err := hn.WalletBalance(ctx, req)
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

	err := wait.Predicate(doesBalanceMatch, 30*time.Second)
	if err != nil {
		return fmt.Errorf("balances not synced after deadline: "+
			"expected %v, only have %v", expectedBalance, lastBalance)
	}

	return nil
}
