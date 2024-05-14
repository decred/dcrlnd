package remotedcrwallet

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"

	base "decred.org/dcrwallet/v4/wallet"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
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

type mockOnchainAddrSourcer struct {
	w *base.Wallet
}

func (mas *mockOnchainAddrSourcer) NewAddress(t lnwallet.AddressType, change bool,
	accountName string) (stdaddr.Address, error) {
	var addr stdaddr.Address
	var err error
	if change {
		addr, err = mas.w.NewInternalAddress(context.TODO(), 0)
	} else {
		addr, err = mas.w.NewExternalAddress(context.TODO(), 0)
	}

	if err != nil {
		return nil, err
	}

	// Convert to a regular p2pkh address, since the addresses returned are
	// used as paramaters to PayT.(stdaddr.Hash160er).Hash160()[:] which doesn't understand
	// the native wallet types.
	return stdaddr.DecodeAddress(addr.String(), chaincfg.SimNetParams())

}
func (mas *mockOnchainAddrSourcer) Bip44AddressInfo(addr stdaddr.Address) (uint32, uint32, uint32, error) {
	info, err := mas.w.KnownAddress(context.Background(), addr)
	if err != nil {
		return 0, 0, 0, nil
	}

	switch ma := info.(type) {
	case base.BIP0044Address:
		acct, branch, child := ma.Path()
		return acct, branch, child, nil
	}

	return 0, 0, 0, fmt.Errorf("unkown address type")
}

func createTestWallet() (func(), *hdkeychain.ExtendedKey, *channeldb.DB, onchainAddrSourcer, error) {
	tempDir, err := ioutil.TempDir("", "keyring-lnwallet")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	loader := walletloader.NewLoader(chaincfg.SimNetParams(), tempDir, 20)

	pass := []byte("test")

	birthday := time.Now().Add(-time.Hour * 24)
	baseWallet, err := loader.CreateNewWallet(
		context.Background(), pass, pass, testHDSeed[:], birthday,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	if err := baseWallet.Unlock(context.Background(), pass, nil); err != nil {
		return nil, nil, nil, nil, err
	}

	// Create the temp chandb dir.
	cdbDir, err := ioutil.TempDir("", "channeldb")
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Next, create channeldb for the first time.
	cdb, err := channeldb.Open(cdbDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// The root master xpriv is the default account's one.
	acctXpriv, err := baseWallet.AccountXpriv(context.Background(), 0)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Create the mock onchain addresses sourcer linked to the previously
	// created wallet.
	addrSourcer := &mockOnchainAddrSourcer{w: baseWallet}

	cleanUp := func() {
		baseWallet.Lock()
		os.RemoveAll(tempDir)
		cdb.Close()
		os.RemoveAll(cdbDir)
	}

	return cleanUp, acctXpriv, cdb, addrSourcer, nil
}

// TestDcrwalletKeyRingImpl tests whether the walletKeyRing implementation
// conforms to the required interface spec.
func TestDcrwalletKeyRingImpl(t *testing.T) {
	t.Parallel()

	keychain.CheckKeyRingImpl(t,
		func() (string, func(), keychain.KeyRing, error) {
			cleanUp, rootXPriv, cdb, addrSourcer, err := createTestWallet()
			if err != nil {
				t.Fatalf("unable to create wallet: %v", err)
			}

			keyRing, err := newRemoteWalletKeyRing(
				rootXPriv, cdb, addrSourcer,
			)

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
			cleanUp, rootXPriv, cdb, addrSourcer, err := createTestWallet()
			if err != nil {
				t.Fatalf("unable to create wallet: %v", err)
			}

			keyRing, err := newRemoteWalletKeyRing(
				rootXPriv, cdb, addrSourcer,
			)

			return "dcrwallet", cleanUp, keyRing, err
		},
	)

}

func init() {
	// We'll clamp the max range scan to constrain the run time of the
	// private key scan test.
	keychain.MaxKeyRangeScan = 3
}
