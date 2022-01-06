package lntest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os/exec"
	"time"

	pb "decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest/wait"
	rpctest "github.com/decred/dcrtest/dcrdtest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

// setupVotingWallet sets up a minimum voting wallet, so that the simnet used
// for tests can advance past SVH.
func (h *HarnessMiner) setupVotingWallet() error {
	vwCtx, vwCancel := context.WithCancel(h.runCtx)
	vw, err := rpctest.NewVotingWallet(vwCtx, h.Harness)
	if err != nil {
		vwCancel()
		return err
	}

	// Use a custom miner on the voting wallet that ensures simnet blocks
	// are generated as fast as possible without triggering PoW difficulty
	// increases.
	vw.SetMiner(func(ctx context.Context, nb uint32) ([]*chainhash.Hash, error) {
		return rpctest.AdjustedSimnetMiner(ctx, h.Node, nb)
	})

	err = vw.Start(vwCtx)
	if err != nil {
		defer vwCancel()
		return err
	}

	h.votingWallet = vw
	h.votingWalletCancel = vwCancel
	return nil
}

// SetUpChain performs the initial chain setup for integration tests. This
// should be done only once.
func (h *HarnessMiner) SetUpChain() error {
	// Generate the premine block the usual way.
	ctx, cancel := context.WithTimeout(h.runCtx, 30*time.Second)
	defer cancel()
	_, err := h.Node.Generate(ctx, 1)
	if err != nil {
		return fmt.Errorf("unable to generate premine: %v", err)
	}

	// Generate enough blocks so that the network harness can have funds to
	// send to the voting wallet, Alice and Bob.
	_, err = rpctest.AdjustedSimnetMiner(ctx, h.Node, 64)
	if err != nil {
		return fmt.Errorf("unable to init chain: %v", err)
	}

	// Setup a ticket buying/voting dcrwallet, so that the network advances
	// past SVH.
	err = h.setupVotingWallet()
	if err != nil {
		return err
	}

	return nil
}

// ModifyTestCaseName modifies the current test case name.
func (n *NetworkHarness) ModifyTestCaseName(testCase string) {
	n.currentTestCase = testCase
}

func (hn *HarnessNode) LogPrintf(format string, args ...interface{}) error {
	now := time.Now().Format("2006-01-02 15:04:05.999")
	f := now + " ----------: " + format
	hn.AddToLog(f, args...)
	return nil
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
