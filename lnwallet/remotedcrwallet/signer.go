package remotedcrwallet

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
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
	req := &pb.GetTransactionRequest{
		TransactionHash: prevOut.Hash[:],
	}
	resp, err := b.wallet.GetTransaction(context.Background(), req)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, lnwallet.ErrNotMine
		}
		return nil, err
	}

	// With the output retrieved, we'll make an additional check to ensure
	// we actually have control of this output. We do this because the
	// check above only guarantees that the transaction is somehow relevant
	// to us, like in the event of us being the sender of the transaction.
	//
	// Furthermore, we'll only consider our control outputs which were sent
	// to the wallet account that dcrlnd controls.
	var credit *pb.TransactionDetails_Output
	for _, out := range resp.Transaction.Credits {
		if out.Index == prevOut.Index && out.Account == b.account {
			credit = out
			break
		}
	}
	if credit == nil {
		return nil, lnwallet.ErrNotMine
	}

	// TODO(decred) The credit structure should return the script version.
	scriptVersion := uint16(0)

	// Then, we'll populate all of the information required by the struct.
	addressType := lnwallet.UnknownAddressType
	scriptClass := stdscript.DetermineScriptType(scriptVersion, credit.OutputScript)
	switch scriptClass {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		addressType = lnwallet.PubKeyHash
	}

	return &lnwallet.Utxo{
		AddressType: addressType,
		Value: dcrutil.Amount(
			credit.Amount,
		),
		PkScript:      credit.OutputScript,
		Confirmations: int64(resp.Confirmations),
		OutPoint:      *prevOut,
	}, nil
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

	script := signDesc.Output.PkScript
	scriptVersion := signDesc.Output.Version

	// Figure out the branch and index of the key needed to sign this
	// input. This assumes only regular onchain keys are used with
	// ComputeInputScript.
	_, addrs := stdscript.ExtractAddrs(scriptVersion, script,
		b.chainParams)
	if len(addrs) != 1 {
		return nil, fmt.Errorf("wrong number of addresses decoded")
	}

	validAddrReq := &pb.ValidateAddressRequest{
		Address: addrs[0].String(),
	}
	validAddrResp, err := b.wallet.ValidateAddress(context.Background(), validAddrReq)
	if err != nil {
		return nil, err
	}
	if !validAddrResp.IsValid {
		return nil, fmt.Errorf("invalid decoded address")
	}
	if !validAddrResp.IsMine {
		return nil, fmt.Errorf("address is not mine")
	}
	if validAddrResp.AccountNumber != b.account {
		return nil, fmt.Errorf("address is for account not controlled by dcrlnd")
	}

	// Fetch the private key for the given wallet address.
	branchXPriv := b.branchExtXPriv
	if validAddrResp.IsInternal {
		branchXPriv = b.branchIntXPriv
	}
	extPrivKey, err := branchXPriv.Child(validAddrResp.Index)
	if err != nil {
		return nil, err
	}
	serPrivKey, err := extPrivKey.SerializedPrivKey()
	if err != nil {
		return nil, err
	}
	privKey := secp256k1.PrivKeyFromBytes(serPrivKey)

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
		script, signDesc.HashType, privKey.Serialize(),
		dcrec.STEcdsaSecp256k1, true)
	if err != nil {
		return nil, err
	}

	witness, err := p2pkhSigScriptToWitness(scriptVersion, sigScript)
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
func (b *DcrWallet) SignMessage(keyDesc keychain.KeyDescriptor,
	msg []byte, double bool) (*ecdsa.Signature, error) {

	// First attempt to fetch the private key which corresponds to the
	// specified public key.
	privKey, err := b.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	// Double hash and sign the data.
	var digest []byte
	if double {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	sign := ecdsa.Sign(privKey, digest)

	return sign, nil
}

// FundPsbt currently does nothing.
func (b *DcrWallet) FundPsbt(_ *psbt.Packet,
	_ chainfee.AtomPerKByte) (int32, error) {

	return 0, fmt.Errorf("FundPSBT not supported")
}

// FinalizePsbt currently does nothing.
func (b *DcrWallet) FinalizePsbt(_ *psbt.Packet) error {
	return fmt.Errorf("FinalizePsbt not supported")
}
