package testutils

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"sync/atomic"
	"time"

	pb "decred.org/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrd/rpcclient/v6"
	"github.com/decred/dcrlnd/lntest/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

var (
	activeNodes int32
	lastPort    uint32 = 41213
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

func consumeSyncMsgs(syncStream pb.WalletLoaderService_RpcSyncClient, onSyncedChan chan struct{}) {
	for {
		msg, err := syncStream.Recv()
		if err != nil {
			// All errors are final here.
			return
		}
		if msg.Synced {
			onSyncedChan <- struct{}{}
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

func NewCustomTestRemoteDcrwallet(t TB, nodeName, dataDir string,
	hdSeed, privatePass []byte,
	dcrd *rpcclient.ConnConfig) (*grpc.ClientConn, func()) {

	// Save the dcrd ca file in the wallet dir.
	cafile := path.Join(dataDir, "ca.cert")
	ioutil.WriteFile(cafile, dcrd.Certificates, 0644)

	tlsCertPath := path.Join(dataDir, "rpc.cert")
	tlsKeyPath := path.Join(dataDir, "rpc.key")

	// Setup the args to run the underlying dcrwallet.
	id := atomic.AddInt32(&activeNodes, 1)
	port := nextAvailablePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	args := []string{
		"--noinitialload",
		"--debuglevel=debug",
		"--rpcconnect=" + dcrd.Host,
		"--username=" + dcrd.User,
		"--password=" + dcrd.Pass,
		"--cafile=" + cafile,
		"--simnet",
		"--nolegacyrpc",
		"--grpclisten=" + addr,
		"--appdata=" + dataDir,
		"--tlscurve=P-256",
		"--rpccert=" + tlsCertPath,
		"--rpckey=" + tlsKeyPath,
		"--clientcafile=" + tlsCertPath,
	}

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
	err = cmd.Start()
	if err != nil {
		t.Logf("Wallet dir: %s", dataDir)
		t.Fatalf("Unable to start %s dcrwallet: %v", nodeName, err)
	}

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
	conn, err := grpc.DialContext(ctx, addr, opts...)
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

	// Run the rpc syncer.
	req := &pb.RpcSyncRequest{
		NetworkAddress:    dcrd.Host,
		Username:          dcrd.User,
		Password:          []byte(dcrd.Pass),
		Certificate:       dcrd.Certificates,
		DiscoverAccounts:  true,
		PrivatePassphrase: privatePass,
	}
	ctxSync, cancelSync := context.WithCancel(context.Background())
	syncStream, err := loader.RpcSync(ctxSync, req)
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
	}

	return conn, cleanup
}

// NewTestRemoteDcrwallet creates a new dcrwallet process that can be used by a
// remotedcrwallet instance to perform the interface tests. This currently only
// supports running the wallet in rpc sync mode.
//
// This function returns the grpc conn and a cleanup function to close the
// wallet.
func NewTestRemoteDcrwallet(t TB, dcrd *rpcclient.ConnConfig) (*grpc.ClientConn, func()) {
	tempDir, err := ioutil.TempDir("", "test-dcrw")
	if err != nil {
		t.Fatal(err)
	}

	var seed [32]byte
	c, tearDownWallet := NewCustomTestRemoteDcrwallet(t, "remotedcrw", tempDir,
		seed[:], []byte("pass"), dcrd)
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
// This function expects a conn returned by NewTestRemoteDcrwallet.
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
