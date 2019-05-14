package dcrwallet

import (
	"crypto/sha256"
	"fmt"

	"github.com/decred/dcrlnd/keychain"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/hdkeychain"
	"github.com/decred/dcrwallet/errors"
	"github.com/decred/dcrwallet/wallet/v2"
	"github.com/decred/dcrwallet/wallet/v2/udb"
)

// WalletKeyRing is an implementation of both the KeyRing and SecretKeyRing
// interfaces backed by dcrwallet's internal root udb. Internally, we'll be
// using a ScopedKeyManager to do all of our derivations, using the key scope
// and scope addr scehma defined above. Re-using the existing key scope
// construction means that all key derivation will be protected under the root
// seed of the wallet, making each derived key fully deterministic.
type WalletKeyRing struct {
	// wallet is a pointer to the active instance of the dcrwallet core.
	// This is required as we'll need to manually open database
	// transactions in order to derive addresses and lookup relevant keys
	wallet *wallet.Wallet
}

// NewWalletKeyRing creates a new implementation of the
// keychain.SecretKeyRing interface backed by dcrwallet.
//
// NOTE: The passed udb.Manager MUST be unlocked in order for the keychain
// to function.
func NewWalletKeyRing(w *wallet.Wallet) keychain.SecretKeyRing {
	return &WalletKeyRing{
		wallet: w,
	}
}

// createAccountsUpTo creates all accounts representing key families up to (and
// including) the provided argument.
// TODO(decred) extremely inefficient for large keyFam. Ideally dcrwallet
// should support using arbitrary account numbers.
func (b *WalletKeyRing) createAccountsUpTo(keyFam keychain.KeyFamily) error {

	// If this is the multi-sig key family, then we can return early as
	// this is the default account that's created.
	if keyFam == keychain.KeyFamilyMultiSig {
		return nil
	}

	// Otherwise, we'll check if the account already exists, if so, we can
	// once again bail early.
	_, err := b.wallet.AccountName(uint32(keyFam))
	if err == nil {
		return nil
	}

	dcrwLog.Infof("Creating wallet accounts up to %d", keyFam)

	// Figure out all uncreated accounts between 0..keyFam
	accounts, err := b.wallet.Accounts()
	if err != nil {
		return nil
	}
	maxExistAccount := uint32(0)
	for _, acct := range accounts.Accounts {
		if acct.AccountNumber < uint32(keyFam) && acct.AccountNumber > maxExistAccount {
			maxExistAccount = acct.AccountNumber
		}
	}

	for i := maxExistAccount + 1; i <= uint32(keyFam); i++ {
		dcrwLog.Debugf("Creating account %d", i)
		_, err = b.wallet.NextAccount(fmt.Sprintf("%d", i))
		if err != nil {
			return err
		}
	}

	return nil
}

// keyDescriptorForAddress returns the key descriptor for the given wallet
// address. It assumes the address exists and is a P2PKH address, otherwise this
// will error.
func (b *WalletKeyRing) keyDescriptorForAddress(addr dcrutil.Address) (keychain.KeyDescriptor, error) {
	var emptyKeyDesc keychain.KeyDescriptor
	addrInfo, err := b.wallet.AddressInfo(addr)
	if err != nil {
		return emptyKeyDesc, err
	}

	pubAddrInfo, is := addrInfo.(udb.ManagedPubKeyAddress)
	if !is {
		return emptyKeyDesc, fmt.Errorf("generated address is not a ManagedPubKeyAddress")
	}

	pubKey, is := pubAddrInfo.PubKey().(*secp256k1.PublicKey)
	if !is {
		return emptyKeyDesc, fmt.Errorf("generated address is not a secp256k1 address")
	}

	return keychain.KeyDescriptor{
		PubKey: pubKey,
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(pubAddrInfo.Account()),
			Index:  pubAddrInfo.Index(),
		},
	}, nil
}

// DeriveNextKey attempts to derive the *next* key within the key family
// (account in BIP43) specified. This method should return the next external
// child within this branch.
//
// NOTE: This is part of the keychain.KeyRing interface.
func (b *WalletKeyRing) DeriveNextKey(keyFam keychain.KeyFamily) (keychain.KeyDescriptor, error) {
	var (
		addr         dcrutil.Address
		err          error
		emptyKeyDesc keychain.KeyDescriptor
	)

	err = b.createAccountsUpTo(keyFam)
	if err != nil {
		return emptyKeyDesc, err
	}

	// TODO(decred) Confirm use of gapPolicyIgnore
	addr, err = b.wallet.NewExternalAddress(uint32(keyFam), wallet.WithGapPolicyIgnore())
	if err != nil && errors.Is(errors.NotExist, err) {
		// Account corresponding to this family does not exist. Create it.
		err = b.createAccountsUpTo(keyFam)
		if err != nil {
			return emptyKeyDesc, err
		}

		// And re-derive the next address for it.
		addr, err = b.wallet.NewExternalAddress(uint32(keyFam), wallet.WithGapPolicyIgnore())
		if err != nil {
			return emptyKeyDesc, err
		}
	}

	return b.keyDescriptorForAddress(addr)
}

// DeriveKey attempts to derive an arbitrary key specified by the passed
// KeyLocator. This may be used in several recovery scenarios, or when manually
// rotating something like our current default node key.
//
// NOTE: This is part of the keychain.KeyRing interface.
func (b *WalletKeyRing) DeriveKey(keyLoc keychain.KeyLocator) (keychain.KeyDescriptor, error) {
	var emptyKeyDesc keychain.KeyDescriptor

	err := b.createAccountsUpTo(keyLoc.Family)
	if err != nil {
		return emptyKeyDesc, err
	}

	famMasterPub, err := b.wallet.MasterPubKey(uint32(keyLoc.Family))
	if err != nil {
		return emptyKeyDesc, err
	}
	branchMasterPub, err := famMasterPub.Child(udb.ExternalBranch)
	if err != nil {
		return emptyKeyDesc, err
	}
	key, err := branchMasterPub.Child(keyLoc.Index)
	if err != nil {
		return emptyKeyDesc, err
	}
	pubKey, err := key.ECPubKey()
	if err != nil {
		return emptyKeyDesc, err
	}

	return keychain.KeyDescriptor{
		KeyLocator: keyLoc,
		PubKey:     pubKey,
	}, nil
}

// DerivePrivKey attempts to derive the private key that corresponds to the
// passed key descriptor.
//
// NOTE: This is part of the keychain.SecretKeyRing interface.
func (b *WalletKeyRing) DerivePrivKey(keyDesc keychain.KeyDescriptor) (*secp256k1.PrivateKey, error) {

	err := b.createAccountsUpTo(keyDesc.Family)
	if err != nil {
		return nil, err
	}

	// We'll grab the master pub key for the provided account (family) then
	// manually derive the addresses here.
	famMasterPriv, err := b.wallet.MasterPrivKey(uint32(keyDesc.Family))
	if err != nil {
		return nil, err
	}
	famBranchPriv, err := famMasterPriv.Child(udb.ExternalBranch)
	if err != nil {
		return nil, err
	}

	// If the public key isn't set or they have a non-zero index,
	// then we know that the caller instead knows the derivation
	// path for a key.
	if keyDesc.PubKey == nil || keyDesc.Index > 0 {
		privKey, err := famBranchPriv.Child(keyDesc.Index)
		if err != nil {
			return nil, err
		}
		return privKey.ECPrivKey()
	}

	// If the public key isn't nil, then this indicates that we
	// need to scan for the private key, assuming that we know the
	// valid key family.
	for i := 0; i < keychain.MaxKeyRangeScan; i++ {
		// Derive the next key in the range and fetch its
		// managed address.
		privKey, err := famBranchPriv.Child(uint32(i))
		if err == hdkeychain.ErrInvalidChild {
			continue
		}

		if err != nil {
			return nil, err
		}

		pubKey, err := privKey.ECPubKey()
		if err != nil {
			// simply skip invalid keys here
			continue
		}

		if keyDesc.PubKey.IsEqual(pubKey) {
			return privKey.ECPrivKey()
		}
	}

	return nil, keychain.ErrCannotDerivePrivKey
}

// ScalarMult performs a scalar multiplication (ECDH-like operation) between
// the target key descriptor and remote public key. The output returned will be
// the sha256 of the resulting shared point serialized in compressed format. If
// k is our private key, and P is the public key, we perform the following
// operation:
//
//  sx := k*P s := sha256(sx.SerializeCompressed())
//
// NOTE: This is part of the keychain.SecretKeyRing interface.
func (b *WalletKeyRing) ScalarMult(keyDesc keychain.KeyDescriptor,
	pub *secp256k1.PublicKey) ([]byte, error) {

	privKey, err := b.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	s := &secp256k1.PublicKey{}
	x, y := secp256k1.S256().ScalarMult(pub.X, pub.Y, privKey.D.Bytes())
	s.X = x
	s.Y = y

	h := sha256.Sum256(s.SerializeCompressed())

	return h[:], nil
}
