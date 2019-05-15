package dcrwallet

import (
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrwallet/errors"
	base "github.com/decred/dcrwallet/wallet/v2"
	"github.com/decred/dcrwallet/wallet/v2/udb"
)

// FetchInputInfo queries for the WalletController's knowledge of the passed
// outpoint. If the base wallet determines this output is under its control,
// then the original txout should be returned. Otherwise, a non-nil error value
// of ErrNotMine should be returned instead.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) FetchInputInfo(prevOut *wire.OutPoint) (*wire.TxOut, error) {
	var (
		err    error
		output *wire.TxOut
	)

	// First check to see if the output is already within the utxo cache.
	// If so we can return directly saving a disk access.
	b.cacheMtx.RLock()
	if output, ok := b.utxoCache[*prevOut]; ok {
		b.cacheMtx.RUnlock()
		return output, nil
	}
	b.cacheMtx.RUnlock()

	// Otherwise, we manually look up the output within the tx store.
	txid := &prevOut.Hash
	txDetail, err := base.UnstableAPI(b.wallet).TxDetails(txid)
	if err != nil {
		if errors.Is(errors.NotExist, err) {
			return nil, lnwallet.ErrNotMine
		}
		return nil, err
	} else if txDetail == nil {
		return nil, lnwallet.ErrNotMine
	}

	// With the output retrieved, we'll make an additional check to ensure
	// we actually have control of this output. We do this because the check
	// above only guarantees that the transaction is somehow relevant to us,
	// like in the event of us being the sender of the transaction.
	output = txDetail.TxRecord.MsgTx.TxOut[prevOut.Index]
	if _, err := b.fetchOutputAddr(output.Version, output.PkScript); err != nil {
		return nil, err
	}

	b.cacheMtx.Lock()
	b.utxoCache[*prevOut] = output
	b.cacheMtx.Unlock()

	return output, nil
}

// fetchOutputAddr attempts to fetch the managed address corresponding to the
// passed output script. This function is used to look up the proper key which
// should be used to sign a specified input.
func (b *DcrWallet) fetchOutputAddr(scriptVersion uint16, script []byte) (udb.ManagedAddress, error) {
	_, addrs, _, err := txscript.ExtractPkScriptAddrs(scriptVersion, script,
		b.netParams)
	if err != nil {
		return nil, err
	}

	// If the case of a multi-sig output, several address may be extracted.
	// Therefore, we simply select the key for the first address we know
	// of.
	for _, addr := range addrs {
		addr, err := b.wallet.AddressInfo(addr)
		if err == nil {
			return addr, nil
		}
	}

	return nil, lnwallet.ErrNotMine
}

// maybeTweakPrivKey examines the single and double tweak parameters on the
// passed sign descriptor and may perform a mapping on the passed private key
// in order to utilize the tweaks, if populated.
func maybeTweakPrivKey(signDesc *lnwallet.SignDescriptor,
	privKey *secp256k1.PrivateKey) (*secp256k1.PrivateKey, error) {

	var retPriv *secp256k1.PrivateKey
	switch {

	case signDesc.SingleTweak != nil:
		retPriv = lnwallet.TweakPrivKey(privKey,
			signDesc.SingleTweak)

	case signDesc.DoubleTweak != nil:
		retPriv = lnwallet.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)

	default:
		retPriv = privKey
	}

	return retPriv, nil
}

// SignOutputRaw generates a signature for the passed transaction according to
// the data within the passed SignDescriptor.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) SignOutputRaw(tx *wire.MsgTx,
	signDesc *lnwallet.SignDescriptor) ([]byte, error) {

	witnessScript := signDesc.WitnessScript

	// First attempt to fetch the private key which corresponds to the
	// specified public key.
	privKey, err := b.DerivePrivKey(signDesc.KeyDesc)
	if err != nil {
		return nil, err
	}

	// If a tweak (single or double) is specified, then we'll need to use
	// this tweak to derive the final private key to be used for signing
	// this output.
	privKey, err = maybeTweakPrivKey(signDesc, privKey)
	if err != nil {
		return nil, err
	}

	// TODO(roasbeef): generate sighash midstate if not present?
	// TODO(decred): use cached prefix hash in signDesc.sigHashes

	sig, err := txscript.RawTxInSignature(tx, signDesc.InputIndex,
		witnessScript, signDesc.HashType, privKey)
	if err != nil {
		return nil, err
	}

	// Chop off the sighash flag at the end of the signature.
	return sig[:len(sig)-1], nil
}

// ComputeInputScript generates a complete InputScript for the passed
// transaction with the signature as defined within the passed SignDescriptor.
// This method is capable of generating the proper input script only for
// regular p2pkh outputs.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ComputeInputScript(tx *wire.MsgTx,
	signDesc *lnwallet.SignDescriptor) (*lnwallet.InputScript, error) {

	outputScript := signDesc.Output.PkScript
	outputScriptVer := signDesc.Output.Version
	walletAddr, err := b.fetchOutputAddr(outputScriptVer, outputScript)
	if err != nil {
		return nil, err
	}

	// Fetch the private key for the given wallet address.
	privKeyWifStr, err := b.wallet.DumpWIFPrivateKey(walletAddr.Address())
	if err != nil {
		return nil, fmt.Errorf("invalid wif string for address: %v", err)
	}
	privKeyWif, err := dcrutil.DecodeWIF(privKeyWifStr)
	if err != nil {
		return nil, fmt.Errorf("error decoding wif string for address: %v", err)
	}
	privKey, isSecp := privKeyWif.PrivKey.(*secp256k1.PrivateKey)
	if !isSecp {
		return nil, fmt.Errorf("private key returned is not secp256k1")
	}

	// If a tweak (single or double) is specified, then we'll need to use
	// this tweak to derive the final private key to be used for signing
	// this output.
	privKey, err = maybeTweakPrivKey(signDesc, privKey)
	if err != nil {
		return nil, err
	}

	// Generate a valid witness stack for the input.
	// TODO(roasbeef): adhere to passed HashType
	scriptSig, err := txscript.SignatureScript(tx, signDesc.InputIndex,
		outputScript, signDesc.HashType, privKey, true)
	if err != nil {
		return nil, err
	}

	return &lnwallet.InputScript{ScriptSig: scriptSig}, nil
}

// A compile time check to ensure that DcrWallet implements the Signer
// interface.
var _ lnwallet.Signer = (*DcrWallet)(nil)

// SignMessage attempts to sign a target message with the private key that
// corresponds to the passed public key. If the target private key is unable to
// be found, then an error will be returned. The actual digest signed is the
// chainhash (blake256r14) of the passed message.
//
// NOTE: This is a part of the MessageSigner interface.
func (b *DcrWallet) SignMessage(pubKey *secp256k1.PublicKey,
	msg []byte) (*secp256k1.Signature, error) {

	keyDesc := keychain.KeyDescriptor{
		PubKey: pubKey,
	}

	// First attempt to fetch the private key which corresponds to the
	// specified public key.
	privKey, err := b.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	// Double hash and sign the data.
	msgDigest := chainhash.HashB(msg)
	sign, err := privKey.Sign(msgDigest)
	if err != nil {
		return nil, errors.Errorf("unable sign the message: %v", err)
	}

	return sign, nil
}

// A compile time check to ensure that DcrWallet implements the MessageSigner
// interface.
var _ lnwallet.MessageSigner = (*DcrWallet)(nil)
