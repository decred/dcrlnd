package dcrwallet

import (
	"context"
	"fmt"

	"decred.org/dcrwallet/v5/errors"
	base "decred.org/dcrwallet/v5/wallet"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/btcwalletcompat"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/internal/psbt"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

// FetchInputInfo queries for the WalletController's knowledge of the passed
// outpoint. If the base wallet determines this output is under its control,
// then the original txout should be returned. Otherwise, a non-nil error value
// of ErrNotMine should be returned instead.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) FetchInputInfo(prevOut *wire.OutPoint) (*lnwallet.Utxo, error) {
	// We manually look up the output within the tx store.
	txid := &prevOut.Hash
	txDetail, err := base.UnstableAPI(b.wallet).TxDetails(context.TODO(), txid)
	if err != nil {
		if errors.Is(err, errors.NotExist) {
			return nil, lnwallet.ErrNotMine
		}
		return nil, err
	} else if txDetail == nil {
		return nil, lnwallet.ErrNotMine
	}

	numOutputs := uint32(len(txDetail.TxRecord.MsgTx.TxOut))
	if prevOut.Index >= numOutputs {
		return nil, fmt.Errorf("invalid output index %v for "+
			"transaction with %v outputs", prevOut.Index, numOutputs)

	}

	// With the output retrieved, we'll make an additional check to ensure
	// we actually have control of this output. We do this because the check
	// above only guarantees that the transaction is somehow relevant to us,
	// like in the event of us being the sender of the transaction.
	output := txDetail.TxRecord.MsgTx.TxOut[prevOut.Index]
	if _, err := b.fetchOutputAddr(output.Version, output.PkScript); err != nil {
		return nil, err
	}

	// Then, we'll populate all of the information required by the struct.
	addressType := lnwallet.UnknownAddressType
	scriptClass := stdscript.DetermineScriptType(output.Version, output.PkScript)
	switch scriptClass {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		addressType = lnwallet.PubKeyHash
	}

	// Determine the number of confirmations the output currently has.
	_, currentHeight := b.wallet.MainChainTip(context.TODO())
	confs := int64(0)
	if txDetail.Block.Height != -1 {
		confs = int64(currentHeight - txDetail.Block.Height)

	}

	return &lnwallet.Utxo{
		AddressType: addressType,
		Value: dcrutil.Amount(
			txDetail.TxRecord.MsgTx.TxOut[prevOut.Index].Value,
		),
		PkScript:      output.PkScript,
		Confirmations: confs,
		OutPoint:      *prevOut,
		PrevTx:        &txDetail.MsgTx,
	}, nil

}

// fetchOutputAddr attempts to fetch the managed address corresponding to the
// passed output script. This function is used to look up the proper key which
// should be used to sign a specified input.
func (b *DcrWallet) fetchOutputAddr(scriptVersion uint16, script []byte) (base.KnownAddress, error) {
	_, addrs := stdscript.ExtractAddrs(scriptVersion, script,
		b.netParams)

	// If the case of a multi-sig output, several address may be extracted.
	// Therefore, we simply select the key for the first address we know
	// of.
	for _, addr := range addrs {
		addr, err := b.wallet.KnownAddress(context.TODO(), addr)
		if err == nil {
			return addr, nil
		}
	}

	return nil, lnwallet.ErrNotMine
}

// maybeTweakPrivKey examines the single and double tweak parameters on the
// passed sign descriptor and may perform a mapping on the passed private key
// in order to utilize the tweaks, if populated.
func maybeTweakPrivKey(signDesc *input.SignDescriptor,
	privKey *secp256k1.PrivateKey) (*secp256k1.PrivateKey, error) {

	var retPriv *secp256k1.PrivateKey
	switch {

	case signDesc.SingleTweak != nil:
		retPriv = input.TweakPrivKey(privKey,
			signDesc.SingleTweak)

	case signDesc.DoubleTweak != nil:
		retPriv = input.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)

	default:
		retPriv = privKey
	}

	return retPriv, nil
}

// SignOutputRaw generates a signature for the passed transaction according to
// the data within the passed input.SignDescriptor.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) SignOutputRaw(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (input.Signature, error) {

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
	sig, err := sign.RawTxInSignature(
		tx, signDesc.InputIndex,
		witnessScript, signDesc.HashType, privKey.Serialize(),
		dcrec.STEcdsaSecp256k1,
	)
	if err != nil {
		return nil, err
	}

	// Chop off the sighash flag at the end of the signature.
	return ecdsa.ParseDERSignature(sig[:len(sig)-1])
}

// p2pkhSigScriptToWitness converts a raw sigScript that signs a p2pkh output
// into a list of individual data pushes, amenable to be used a pseudo-witness
// stack.
func p2pkhSigScriptToWitness(scriptVersion uint16, sigScript []byte) ([][]byte, error) {
	data := make([][]byte, 0, 2)
	tokenizer := txscript.MakeScriptTokenizer(scriptVersion, sigScript)
	for tokenizer.Next() && len(data) < 2 {
		if tokenizer.Data() == nil {
			return nil, errors.New("expected pushed data in p2pkh " +
				"sigScript but found none")
		}
		data = append(data, tokenizer.Data())
	}
	if err := tokenizer.Err(); err != nil {
		return nil, err

	}
	if len(data) != 2 {
		return nil, fmt.Errorf("expected p2pkh sigScript to have 2 "+
			"data pushes but found %d", len(data))
	}
	return data, nil

}

// ComputeInputScript generates a complete InputScript for the passed
// transaction with the signature as defined within the passed input.SignDescriptor.
// This method is capable of generating the proper input script only for
// regular p2pkh outputs.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ComputeInputScript(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (*input.Script, error) {

	outputScript := signDesc.Output.PkScript
	outputScriptVer := signDesc.Output.Version
	walletAddr, err := b.fetchOutputAddr(outputScriptVer, outputScript)
	if err != nil {
		return nil, err
	}

	// Fetch the private key for the given wallet address.
	addr, err := stdaddr.DecodeAddress(walletAddr.String(), b.netParams)
	if err != nil {
		return nil, err
	}
	privKeyWifStr, err := b.wallet.DumpWIFPrivateKey(context.TODO(), addr)
	if err != nil {
		return nil, fmt.Errorf("invalid wif string for address: %v", err)
	}
	privKeyWif, err := dcrutil.DecodeWIF(privKeyWifStr, b.netParams.PrivateKeyID)
	if err != nil {
		return nil, fmt.Errorf("error decoding wif string for address: %v", err)
	}
	if privKeyWif.DSA() != dcrec.STEcdsaSecp256k1 {
		return nil, fmt.Errorf("private key returned is not secp256k1")
	}
	privKeyBytes := privKeyWif.PrivKey()
	privKey := secp256k1.PrivKeyFromBytes(privKeyBytes)

	// If a tweak (single or double) is specified, then we'll need to use
	// this tweak to derive the final private key to be used for signing
	// this output.
	privKey, err = maybeTweakPrivKey(signDesc, privKey)
	if err != nil {
		return nil, err
	}

	// Generate a valid witness stack for the input.
	// TODO(roasbeef): adhere to passed HashType
	sigScript, err := sign.SignatureScript(tx, signDesc.InputIndex,
		outputScript, signDesc.HashType, privKey.Serialize(),
		dcrec.STEcdsaSecp256k1, true)
	if err != nil {
		return nil, err
	}

	witness, err := p2pkhSigScriptToWitness(outputScriptVer, sigScript)
	if err != nil {
		return nil, err
	}

	return &input.Script{Witness: witness}, nil
}

// A compile time check to ensure that DcrWallet implements the input.Signer
// interface.
var _ input.Signer = (*DcrWallet)(nil)

// SignMessage attempts to sign a target message with the private key that
// corresponds to the passed public key. If the target private key is unable to
// be found, then an error will be returned. The actual digest signed is the
// chainhash (blake256r14) of the passed message.
//
// NOTE: This is a part of the Messageinput.Signer interface.
func (b *DcrWallet) SignMessage(keyDesc keychain.KeyLocator,
	msg []byte, double bool) (*ecdsa.Signature, error) {

	// First attempt to fetch the private key which corresponds to the
	// specified public key.
	privKey, err := b.DerivePrivKey(keychain.KeyDescriptor{KeyLocator: keyDesc})
	if err != nil {
		return nil, err
	}

	// Double hash and sign the data.
	var digest []byte
	if double {
		return nil, fmt.Errorf("dcrlnd does not do doubleHash signing")
	} else {
		digest = chainhash.HashB(msg)
	}
	sign := ecdsa.Sign(privKey, digest)

	return sign, nil
}

// FundPsbt currently does nothing.
func (b *DcrWallet) FundPsbt(_ *psbt.Packet, _ int32,
	_ chainfee.AtomPerKByte, _ string) (int32, error) {

	return 0, fmt.Errorf("FundPSBT not supported")
}

// FinalizePsbt currently does nothing.
func (b *DcrWallet) FinalizePsbt(_ *psbt.Packet) error {
	return fmt.Errorf("FinalizePsbt not supported")
}

// SignPsbt does nothing.
func (b *DcrWallet) SignPsbt(*psbt.Packet) error {
	return fmt.Errorf("SignPsbt not supported")
}

// AddressInfo returns info about a wallet address.
func (b *DcrWallet) AddressInfo(
	stdaddr.Address) (btcwalletcompat.ManagedAddress, error) {

	return nil, fmt.Errorf("AddressInfo not supported")
}
