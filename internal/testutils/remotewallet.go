package testutils

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"sync/atomic"
	"time"

	pb "decred.org/dcrwallet/v4/rpc/walletrpc"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrlnd/lntest/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

var activeNodes int32

type rpcSyncer struct {
	c pb.WalletLoaderService_RpcSyncClient
}

func (r *rpcSyncer) RecvSynced() (bool, error) {
	msg, err := r.c.Recv()
	if err != nil {
		// All errors are final here.
		return false, err
	}
	return msg.Synced, nil
}

type spvSyncer struct {
	c pb.WalletLoaderService_SpvSyncClient
}

func (r *spvSyncer) RecvSynced() (bool, error) {
	msg, err := r.c.Recv()
	if err != nil {
		// All errors are final here.
		return false, err
	}
	return msg.Synced, nil
}

type syncer interface {
	RecvSynced() (bool, error)
}

func consumeSyncMsgs(syncStream syncer, onSyncedChan chan struct{}) {
	for {
		synced, err := syncStream.RecvSynced()
		if err != nil {
			// All errors are final here.
			return
		}
		if synced {
			onSyncedChan <- struct{}{}
			return
		}
	}
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

type SPVConfig struct {
	Address string
}

// NewCustomTestRemoteDcrwallet runs a dcrwallet instance for use during tests.
func NewCustomTestRemoteDcrwallet(t TB, nodeName, dataDir string,
	hdSeed, privatePass []byte,
	dcrd *rpcclient.ConnConfig, spv *SPVConfig) (*grpc.ClientConn, func()) {

	if dcrd == nil && spv == nil {
		t.Fatalf("either dcrd or spv config needs to be specified")
	}
	if dcrd != nil && spv != nil {
		t.Fatalf("only one of dcrd or spv config needs to be specified")
	}

	tlsCertPath := path.Join(dataDir, "rpc.cert")
	tlsKeyPath := path.Join(dataDir, "rpc.key")

	pipeTX, err := newIPCPipePair(true, false)
	if err != nil {
		t.Fatalf("unable to create pipe for dcrd IPC: %v", err)
	}
	pipeRX, err := newIPCPipePair(false, true)
	if err != nil {
		t.Fatalf("unable to create pipe for dcrd IPC: %v", err)
	}

	// Setup the args to run the underlying dcrwallet.
	id := atomic.AddInt32(&activeNodes, 1)
	args := []string{
		"--noinitialload",
		"--debuglevel=debug",
		"--simnet",
		"--nolegacyrpc",
		"--grpclisten=127.0.0.1:0",
		"--appdata=" + dataDir,
		"--tlscurve=P-256",
		"--rpccert=" + tlsCertPath,
		"--rpckey=" + tlsKeyPath,
		"--clientcafile=" + tlsCertPath,
		"--rpclistenerevents",
	}
	args = appendOSWalletArgs(&pipeTX, &pipeRX, args)

	logFilePath := path.Join(fmt.Sprintf("output-remotedcrw-%.2d-%s.log",
		id, nodeName))
	logFile, err := os.Create(logFilePath)
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("Unable to create %s dcrwallet log file: %v",
			nodeName, err)
	}

	const dcrwalletExe = "dcrwallet-dcrlnd"

	// Run dcrwallet.
	cmd := exec.Command(dcrwalletExe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setOSWalletCmdOptions(&pipeTX, &pipeRX, cmd)
	err = cmd.Start()
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("Unable to start %s dcrwallet: %v", nodeName, err)
	}

	// Read the subsystem addresses.
	gotSubsysAddrs := make(chan struct{})
	var grpcAddr string
	go func() {
		for grpcAddr == "" {
			msg, err := nextIPCMessage(pipeTX.r)
			if err != nil {
				t.Logf("Unable to read next IPC message: %v", err)
				return
			}
			switch msg := msg.(type) {
			case boundGRPCListenAddrEvent:
				grpcAddr = string(msg)
				close(gotSubsysAddrs)
			}
		}

		// Drain messages until the pipe is closed.
		var err error
		for err == nil {
			_, err = nextIPCMessage(pipeRX.r)
		}
	}()

	// Read the wallet TLS cert and client cert and key files.
	var caCert *x509.CertPool
	var clientCert tls.Certificate
	err = wait.NoError(func() error {
		var err error
		caCert, err = tlsCertFromFile(tlsCertPath)
		if err != nil {
			return fmt.Errorf("unable to load wallet ca cert: %v", err)
		}

		clientCert, err = tls.LoadX509KeyPair(tlsCertPath, tlsKeyPath)
		if err != nil {
			return fmt.Errorf("unable to load wallet cert and key files: %v", err)
		}

		return nil
	}, time.Second*30)
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("Unable to read ca cert file: %v", err)
	}

	// Wait until the gRPC address is read via IPC.
	select {
	case <-gotSubsysAddrs:
	case <-time.After(time.Second * 30):
		t.Fatalf("wallet did not send gRPC address through IPC")
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
	ctxb := context.Background()
	ctx, cancel := context.WithTimeout(ctxb, time.Second*30)
	defer cancel()
	conn, err := grpc.DialContext(ctx, grpcAddr, opts...)
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("Unable to dial grpc: %v", err)
	}

	loader := pb.NewWalletLoaderServiceClient(conn)

	// Create the wallet.
	reqCreate := &pb.CreateWalletRequest{
		Seed:              hdSeed,
		PublicPassphrase:  privatePass,
		PrivatePassphrase: privatePass,
	}
	ctx, cancel = context.WithTimeout(ctxb, time.Second*30)
	defer cancel()
	_, err = loader.CreateWallet(ctx, reqCreate)
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("unable to create wallet: %v", err)
	}

	ctxSync, cancelSync := context.WithCancel(context.Background())
	var syncStream syncer
	if dcrd != nil {
		// Run the rpc syncer.
		req := &pb.RpcSyncRequest{
			NetworkAddress:    dcrd.Host,
			Username:          dcrd.User,
			Password:          []byte(dcrd.Pass),
			Certificate:       dcrd.Certificates,
			DiscoverAccounts:  true,
			PrivatePassphrase: privatePass,
		}
		var res pb.WalletLoaderService_RpcSyncClient
		res, err = loader.RpcSync(ctxSync, req)
		syncStream = &rpcSyncer{c: res}
	} else if spv != nil {
		// Run the spv syncer.
		req := &pb.SpvSyncRequest{
			SpvConnect:        []string{spv.Address},
			DiscoverAccounts:  true,
			PrivatePassphrase: privatePass,
		}
		var res pb.WalletLoaderService_SpvSyncClient
		res, err = loader.SpvSync(ctxSync, req)
		syncStream = &spvSyncer{c: res}
	}
	if err != nil {
		cancelSync()
		t.Fatalf("error running rpc sync: %v", err)
	}

	// Wait for the wallet to sync. Remote wallets are assumed synced
	// before an ln wallet is started for them.
	onSyncedChan := make(chan struct{})
	go consumeSyncMsgs(syncStream, onSyncedChan)
	select {
	case <-onSyncedChan:
		// Sync done.
	case <-time.After(time.Second * 60):
		cancelSync()
		t.Fatalf("timeout waiting for initial sync to complete")
	}

	cleanup := func() {
		cancelSync()

		if cmd.ProcessState != nil {
			return
		}

		if t.Failed() {
			t.Logf("Wallet data at %s", dataDir)
		}

		err := cmd.Process.Signal(os.Interrupt)
		if err != nil {
			t.Errorf("Error sending SIGINT to %s dcrwallet: %v",
				nodeName, err)
			return
		}

		// Wait for dcrwallet to exit or force kill it after a timeout.
		// For this, we run the wait on a goroutine and signal once it
		// has returned.
		errChan := make(chan error)
		go func() {
			errChan <- cmd.Wait()
		}()

		select {
		case err := <-errChan:
			if err != nil {
				t.Errorf("%s dcrwallet exited with an error: %v",
					nodeName, err)
			}

		case <-time.After(time.Second * 15):
			t.Errorf("%s dcrwallet timed out after SIGINT", nodeName)
			err := cmd.Process.Kill()
			if err != nil {
				t.Errorf("Error killing %s dcrwallet: %v",
					nodeName, err)
			}
		}

		pipeTX.close()
		pipeRX.close()
	}

	return conn, cleanup
}

// NewRPCSyncingTestRemoteDcrwallet creates a new dcrwallet process that can be
// used by a remotedcrwallet instance to perform the interface tests. This
// remote wallet syncs to the passed dcrd node using RPC mode sycing.
//
// This function returns the grpc conn and a cleanup function to close the
// wallet.
func NewRPCSyncingTestRemoteDcrwallet(t TB, dcrd *rpcclient.ConnConfig) (*grpc.ClientConn, func()) {
	tempDir, err := ioutil.TempDir("", "test-dcrw-rpc")
	if err != nil {
		t.Fatal(err)
	}

	var seed [32]byte
	c, tearDownWallet := NewCustomTestRemoteDcrwallet(t, "remotedcrw", tempDir,
		seed[:], []byte("pass"), dcrd, nil)
	tearDown := func() {
		tearDownWallet()

		if !t.Failed() {
			os.RemoveAll(tempDir)
		}
	}

	return c, tearDown
}

// NewRPCSyncingTestRemoteDcrwallet creates a new dcrwallet process that can be
// used by a remotedcrwallet instance to perform the interface tests. This
// remote wallet syncs to the passed dcrd node using SPV mode sycing.
//
// This function returns the grpc conn and a cleanup function to close the
// wallet.
func NewSPVSyncingTestRemoteDcrwallet(t TB, p2pAddr string) (*grpc.ClientConn, func()) {
	tempDir, err := ioutil.TempDir("", "test-dcrw-spv")
	if err != nil {
		t.Fatal(err)
	}

	var seed [32]byte
	c, tearDownWallet := NewCustomTestRemoteDcrwallet(t, "remotedcrw", tempDir,
		seed[:], []byte("pass"), nil, &SPVConfig{Address: p2pAddr})
	tearDown := func() {
		tearDownWallet()

		if !t.Failed() {
			os.RemoveAll(tempDir)
		}
	}

	return c, tearDown
}

// SetPerAccountPassphrase calls the SetAccountPassphrase rpc endpoint on the
// wallet at the given conn, setting it to the specified passphrse.
//
// This function expects a conn returned by NewCustomTestRemoteDcrwallet.
func SetPerAccountPassphrase(conn *grpc.ClientConn, passphrase []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// Set the wallet to use per-account passphrases.
	wallet := pb.NewWalletServiceClient(conn)
	reqSetAcctPwd := &pb.SetAccountPassphraseRequest{
		AccountNumber:        0,
		WalletPassphrase:     passphrase,
		NewAccountPassphrase: passphrase,
	}
	_, err := wallet.SetAccountPassphrase(ctx, reqSetAcctPwd)
	return err
}
