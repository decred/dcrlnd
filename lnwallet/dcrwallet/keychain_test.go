package dcrwallet

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"

	wallet "decred.org/dcrwallet/v4/wallet"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
)

var (
	testHDSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x98, 0xa3, 0xef, 0xb9,
		0x69, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}
)

func createTestWallet() (func(), *wallet.Wallet, *channeldb.DB, error) {
	tempDir, err := ioutil.TempDir("", "keyring-lnwallet")
	if err != nil {
		return nil, nil, nil, err
	}
	loader := walletloader.NewLoader(chaincfg.SimNetParams(), tempDir,
		wallet.DefaultGapLimit)

	pass := []byte("test")

	baseWallet, err := loader.CreateNewWallet(
		context.Background(), pass, pass, testHDSeed[:],
	)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := baseWallet.Unlock(context.Background(), pass, nil); err != nil {
		return nil, nil, nil, err
	}

	// Create the temp chandb dir.
	cdbDir, err := ioutil.TempDir("", "channeldb")
	if err != nil {
		return nil, nil, nil, err
	}

	// Next, create channeldb for the first time.
	cdb, err := channeldb.Open(cdbDir)
	if err != nil {
		return nil, nil, nil, err
	}

	cleanUp := func() {
		baseWallet.Lock()
		os.RemoveAll(tempDir)
		cdb.Close()
		os.RemoveAll(cdbDir)
	}

	return cleanUp, baseWallet, cdb, nil
}

// TestDcrwalletKeyRingImpl tests whether the walletKeyRing implementation
// conforms to the required interface spec.
func TestDcrwalletKeyRingImpl(t *testing.T) {
	t.Parallel()

	keychain.CheckKeyRingImpl(t,
		func() (string, func(), keychain.KeyRing, error) {
			cleanUp, wallet, cdb, err := createTestWallet()
			if err != nil {
				t.Fatalf("unable to create wallet: %v", err)
			}

			keyRing, err := newWalletKeyRing(wallet, cdb)

			return "dcrwallet", cleanUp, keyRing, err
		},
	)

}

// TestDcrwalletSecretKeyRingImpl tests whether the walletKeyRing
// implementation conforms to the required interface spec.
func TestDcrwalletSecretKeyRingImpl(t *testing.T) {
	t.Parallel()

	keychain.CheckSecretKeyRingImpl(t,
		func() (string, func(), keychain.SecretKeyRing, error) {
			cleanUp, wallet, cdb, err := createTestWallet()
			if err != nil {
				t.Fatalf("unable to create wallet: %v", err)
			}

			keyRing, err := newWalletKeyRing(wallet, cdb)

			return "dcrwallet", cleanUp, keyRing, err
		},
	)

}

func init() {
	// We'll clamp the max range scan to constrain the run time of the
	// private key scan test.
	keychain.MaxKeyRangeScan = 3
}
