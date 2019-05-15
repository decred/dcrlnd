package dcrwallet

import (
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"

	"github.com/decred/dcrd/hdkeychain"
	"github.com/decred/dcrwallet/wallet/v2"
	"github.com/decred/dcrwallet/wallet/v2/udb"
)

// walletKeyRing is an implementation of both the KeyRing and SecretKeyRing
// interfaces backed by dcrwallet's internal root keys.
//
// While the wallet's root keys are used, the actual final key derivation does
// _not_ take place in the wallet; instead it is done here. This is done so
// that the wallet does not attempt to keep track of a large amount of keys
// (addresses) that are meant for off-chain processes and are unlikely to ever
// be found on-chain.
//
// Even though the final keys are derived here, they are currently derived
// following a BIP0043 style path starting at the first account (account number
// 0) and for the external branch only, which maintains compatibility with a
// previous version of this implementation that was done entirely on the
// wallet. Note that changing this derivation procedure means invalidating
// existing dcrlnd wallets.
type walletKeyRing struct {
	*keychain.HDKeyRing

	// wallet is a pointer to the active instance of the dcrwallet core.
	// This is required as we'll need to manually open database
	// transactions in order to derive addresses and lookup relevant keys
	wallet *wallet.Wallet

	// db is a pointer to a channeldb.DB instance that stores the indices
	// of used keys of the keyring. This is used to track the next
	// available key to prevent key reuse when establishing channels.
	db *channeldb.DB
}

// Compile time type assertions to ensure walletKeyRing fulfills the desired
// interfaces.
var _ keychain.KeyRing = (*walletKeyRing)(nil)
var _ keychain.SecretKeyRing = (*walletKeyRing)(nil)

// newWalletKeyRing creates a new implementation of the keychain.SecretKeyRing
// interface backed by dcrwallet.
//
// NOTE: The passed wallet MUST be unlocked in order for the keychain to
// function.
func newWalletKeyRing(w *wallet.Wallet, db *channeldb.DB) (*walletKeyRing, error) {

	// This assumes that there are no discontinuities within the KeyFamily
	// constants.
	lastKeyFam := uint32(keychain.KeyFamilyNodeKey)
	masterPubs := make(map[keychain.KeyFamily]*hdkeychain.ExtendedKey,
		lastKeyFam+1)

	ctKey, err := w.CoinTypePrivKey()
	if err != nil {
		return nil, err
	}

	// Derive the master pubs for each key family. They are mapped to the
	// external branch for each corresponding wallet account.
	for i := uint32(0); i <= lastKeyFam; i++ {
		// Errors here cause fatal failures due to the wallet not
		// attempting to generate the next account.
		acctKey, err := ctKey.Child(hdkeychain.HardenedKeyStart + i)
		if err != nil {
			return nil, err
		}

		branchKey, err := acctKey.Child(udb.ExternalBranch)
		if err != nil {
			return nil, err
		}

		branchPub, err := branchKey.Neuter()
		if err != nil {
			return nil, err
		}

		masterPubs[keychain.KeyFamily(i)] = branchPub
	}

	wkr := &walletKeyRing{
		wallet: w,
		db:     db,
	}
	wkr.HDKeyRing = keychain.NewHDKeyRing(masterPubs, wkr.fetchMasterPriv,
		wkr.nextIndex)
	return wkr, nil
}

func (wkr *walletKeyRing) nextIndex(keyFam keychain.KeyFamily) (uint32, error) {
	return wkr.db.NextKeyFamilyIndex(uint32(keyFam))
}

func (wkr *walletKeyRing) fetchMasterPriv(keyFam keychain.KeyFamily) (*hdkeychain.ExtendedKey,
	error) {

	ctKey, err := wkr.wallet.CoinTypePrivKey()
	if err != nil {
		return nil, err
	}

	acctKey, err := ctKey.Child(hdkeychain.HardenedKeyStart + uint32(keyFam))
	if err != nil {
		return nil, err
	}

	branchKey, err := acctKey.Child(udb.ExternalBranch)
	if err != nil {
		return nil, err
	}

	return branchKey, nil
}
