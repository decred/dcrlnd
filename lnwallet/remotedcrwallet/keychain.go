package remotedcrwallet

import (
	"errors"

	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
)

// onchainAddrSourcer is an interface for the required operations needed to
// derive keys that also correspond to regular onchain wallet addresses.
type onchainAddrSourcer interface {
	// NewAddress must return the next usable onchain address for the
	// wallet.
	NewAddress(t lnwallet.AddressType, change bool, accountName string) (stdaddr.Address, error)

	// Bip44AddressInfo returns the respective account, branch and index
	// for the given wallet address.
	Bip44AddressInfo(addr stdaddr.Address) (uint32, uint32, uint32, error)
}

// remoteWalletKeyRing is an implementation of both the KeyRing and
// SecretKeyRing interfaces backed by a root master HD key.
type remoteWalletKeyRing struct {
	*keychain.HDKeyRing

	onchainAddrs onchainAddrSourcer

	rootXPriv          *hdkeychain.ExtendedKey
	multiSigKFXpriv    *hdkeychain.ExtendedKey
	paymentBaseKFXpriv *hdkeychain.ExtendedKey

	// db is a pointer to a channeldb.DB instance that stores the indices
	// of used keys of the keyring. This is used to track the next
	// available key to prevent key reuse when establishing channels.
	db *channeldb.DB
}

// Compile time type assertions to ensure remoteWalletKeyRing fulfills the desired
// interfaces.
var _ keychain.KeyRing = (*remoteWalletKeyRing)(nil)
var _ keychain.SecretKeyRing = (*remoteWalletKeyRing)(nil)

// newRemoteWalletKeyRing creates a new implementation of the
// keychain.SecretKeyRing interface backed by the given root extended private
// key.
func newRemoteWalletKeyRing(rootAccountXPriv *hdkeychain.ExtendedKey,
	db *channeldb.DB, onchainAddrs onchainAddrSourcer) (*remoteWalletKeyRing, error) {

	if !rootAccountXPriv.IsPrivate() {
		return nil, errors.New("Provided key is not an extended private key")
	}

	// By convention, the root LN key is a hardened key derived from index
	// 1017 of a wallet account.
	idx := uint32(hdkeychain.HardenedKeyStart + keychain.BIP0043Purpose)
	rootXPriv, err := rootAccountXPriv.Child(idx)
	if err != nil {
		return nil, err
	}

	// This assumes that there are no discontinuities within the KeyFamily
	// constants.
	lastKeyFam := uint32(keychain.KeyFamilyLastKF)
	masterPubs := make(map[keychain.KeyFamily]*hdkeychain.ExtendedKey,
		lastKeyFam+1)

	// The masterpub for the multisig key family is still the external
	// branch for the root account xpriv provided. This allows the wallet
	// itself to watch for on-chain spends from it and correctly account
	// for its funds.
	multiSigXPriv, err := rootAccountXPriv.Child(0)
	if err != nil {
		return nil, err
	}
	multiSigXPub := multiSigXPriv.Neuter()
	masterPubs[keychain.KeyFamilyMultiSig] = multiSigXPub

	// The masterpub for the payment base key family (i.e. addresses used
	// for our non-encumbered output in remote commitments) is the internal
	// branch for the root account xpriv provided. This allows the wallet
	// to directly spend these funds when a breach or DLP scenario is
	// triggered without requiring any other off-chain state.
	paymentBaseXPriv, err := rootAccountXPriv.Child(0)
	if err != nil {
		return nil, err
	}
	paymentBaseXPub := paymentBaseXPriv.Neuter()
	masterPubs[keychain.KeyFamilyPaymentBase] = paymentBaseXPub

	// Derive the master pubs for the other key families.
	for i := uint32(0); i <= lastKeyFam; i++ {
		switch keychain.KeyFamily(i) {
		case keychain.KeyFamilyMultiSig:
			continue
		case keychain.KeyFamilyPaymentBase:
			continue
		}

		// Errors here cause fatal failures due to the wallet not
		// attempting to generate the next account.
		famKey, err := rootXPriv.Child(hdkeychain.HardenedKeyStart + i)
		if err != nil {
			return nil, err
		}

		famPub := famKey.Neuter()
		masterPubs[keychain.KeyFamily(i)] = famPub
	}

	wkr := &remoteWalletKeyRing{
		rootXPriv:          rootXPriv,
		multiSigKFXpriv:    multiSigXPriv,
		paymentBaseKFXpriv: paymentBaseXPriv,
		db:                 db,
		onchainAddrs:       onchainAddrs,
	}
	wkr.HDKeyRing = keychain.NewHDKeyRing(masterPubs, wkr.fetchMasterPriv,
		wkr.nextIndex)
	return wkr, nil
}

func (kr *remoteWalletKeyRing) nextIndex(keyFam keychain.KeyFamily) (uint32, error) {
	switch keyFam {
	case keychain.KeyFamilyMultiSig,
		keychain.KeyFamilyPaymentBase:

		// For these key families, instead of using the channel
		// database to track indices we request the next available
		// address from the wallet (with the wrap gap policy) and
		// decode its index.
		//
		// The net result is that the wallet should always be able to
		// find these addresses and their respective keys during
		// address discovery instead of requiring input from the
		// lightning wallet.
		branchInternal := keyFam == keychain.KeyFamilyPaymentBase
		addr, err := kr.onchainAddrs.NewAddress(
			lnwallet.PubKeyHash, branchInternal, lnwallet.DefaultAccountName,
		)
		if err != nil {
			return 0, err
		}

		_, _, addrIndex, err := kr.onchainAddrs.Bip44AddressInfo(addr)
		if err != nil {
			return 0, err
		}
		return addrIndex, nil

	}

	return kr.db.NextKeyFamilyIndex(uint32(keyFam))
}

func (kr *remoteWalletKeyRing) fetchMasterPriv(keyFam keychain.KeyFamily) (*hdkeychain.ExtendedKey,
	error) {

	// The master priv of the special key families that correspond to
	// regular on-chain branches are stored separately.
	switch keyFam {
	case keychain.KeyFamilyMultiSig:
		return kr.multiSigKFXpriv, nil
	case keychain.KeyFamilyPaymentBase:
		return kr.paymentBaseKFXpriv, nil
	}

	// For the other keyfamilies, derive from the alternative root branch.
	idx := uint32(hdkeychain.HardenedKeyStart + keyFam)
	famKey, err := kr.rootXPriv.Child(idx)
	if err != nil {
		return nil, err
	}

	return famKey, nil
}
