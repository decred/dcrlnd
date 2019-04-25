package lookout

import (
	"errors"

	"github.com/decred/dcrd/blockchain"
	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/watchtower/blob"
	"github.com/decred/dcrlnd/watchtower/wtdb"
)

var (
	// ErrOutputNotFound signals that the breached output could not be found
	// on the commitment transaction.
	ErrOutputNotFound = errors.New("unable to find output on commit tx")

	// ErrUnknownSweepAddrType signals that client provided an output that
	// was not p2wkh or p2wsh.
	ErrUnknownSweepAddrType = errors.New("sweep addr is not p2wkh or p2wsh")
)

// JusticeDescriptor contains the information required to sweep a breached
// channel on behalf of a victim. It supports the ability to create the justice
// transaction that sweeps the commitments and recover a cut of the channel for
// the watcher's eternal vigilance.
type JusticeDescriptor struct {
	// BreachedCommitTx is the commitment transaction that caused the breach
	// to be detected.
	BreachedCommitTx *wire.MsgTx

	// SessionInfo contains the contract with the watchtower client and
	// the prenegotiated terms they agreed to.
	SessionInfo *wtdb.SessionInfo

	// JusticeKit contains the decrypted blob and information required to
	// construct the transaction scripts and witnesses.
	JusticeKit *blob.JusticeKit

	// NetParams contains a reference to the chain parameters where the
	// transactions will be broadcast.
	NetParams *chaincfg.Params
}

// breachedInput contains the required information to construct and spend
// breached outputs on a commitment transaction.
type breachedInput struct {
	txOut    *wire.TxOut
	outPoint wire.OutPoint
	witness  [][]byte
}

// commitToLocalInput extracts the information required to spend the commit
// to-local output.
func (p *JusticeDescriptor) commitToLocalInput() (*breachedInput, error) {
	// Retrieve the to-local witness script from the justice kit.
	toLocalScript, err := p.JusticeKit.CommitToLocalWitnessScript()
	if err != nil {
		return nil, err
	}

	// Compute the witness script hash, which will be used to locate the
	// input on the breaching commitment transaction.
	toLocalWitnessHash, err := lnwallet.ScriptHashPkScript(toLocalScript)
	if err != nil {
		return nil, err
	}

	// Locate the to-local output on the breaching commitment transaction.
	toLocalIndex, toLocalTxOut, err := findTxOutByPkScript(
		p.BreachedCommitTx, toLocalWitnessHash,
	)
	if err != nil {
		return nil, err
	}

	// Construct the to-local outpoint that will be spent in the justice
	// transaction.
	toLocalOutPoint := wire.OutPoint{
		Hash:  p.BreachedCommitTx.TxHash(),
		Index: toLocalIndex,
	}

	// Retrieve to-local witness stack, which primarily includes a signature
	// under the revocation pubkey.
	witnessStack, err := p.JusticeKit.CommitToLocalRevokeWitnessStack()
	if err != nil {
		return nil, err
	}

	return &breachedInput{
		txOut:    toLocalTxOut,
		outPoint: toLocalOutPoint,
		witness:  buildWitness(witnessStack, toLocalScript),
	}, nil
}

// commitToRemoteInput extracts the information required to spend the commit
// to-remote output.
func (p *JusticeDescriptor) commitToRemoteInput() (*breachedInput, error) {
	// Retrieve the to-remote witness script from the justice kit.
	toRemoteSerPubKey, err := p.JusticeKit.CommitToRemoteWitnessScript()
	if err != nil {
		return nil, err
	}

	// Since the to-remote witness script should just be a regular p2wkh
	// output, we'll parse it to retrieve the public key.
	toRemotePubKey, err := secp256k1.ParsePubKey(toRemoteSerPubKey)
	if err != nil {
		return nil, err
	}

	// Compute the witness script hash from the to-remote pubkey, which will
	// be used to locate the input on the breach commitment transaction.
	toRemotePkScript, err := lnwallet.CommitScriptUnencumbered(
		toRemotePubKey,
	)
	if err != nil {
		return nil, err
	}

	// Locate the to-remote output on the breaching commitment transaction.
	toRemoteIndex, toRemoteTxOut, err := findTxOutByPkScript(
		p.BreachedCommitTx, toRemotePkScript,
	)
	if err != nil {
		return nil, err
	}

	// Construct the to-remote outpoint which will be spent in the justice
	// transaction.
	toRemoteOutPoint := wire.OutPoint{
		Hash:  p.BreachedCommitTx.TxHash(),
		Index: toRemoteIndex,
	}

	// Retrieve the to-remote witness stack, which is just a signature under
	// the to-remote pubkey.
	witnessStack, err := p.JusticeKit.CommitToRemoteWitnessStack()
	if err != nil {
		return nil, err
	}

	return &breachedInput{
		txOut:    toRemoteTxOut,
		outPoint: toRemoteOutPoint,
		witness:  buildWitness(witnessStack, toRemoteSerPubKey),
	}, nil
}

// assembleJusticeTxn accepts the breached inputs recovered from state update
// and attempts to construct the justice transaction that sweeps the victims
// funds to their wallet and claims the watchtower's reward.
func (p *JusticeDescriptor) assembleJusticeTxn(txSize int64,
	inputs ...*breachedInput) (*wire.MsgTx, error) {

	justiceTxn := wire.NewMsgTx()
	justiceTxn.Version = 2

	// First, construct add the breached inputs to our justice transaction
	// and compute the total amount that will be swept.
	var totalAmt dcrutil.Amount
	for _, input := range inputs {
		totalAmt += dcrutil.Amount(input.txOut.Value)
		justiceTxn.AddTxIn(&wire.TxIn{
			PreviousOutPoint: input.outPoint,
			ValueIn:          input.txOut.Value,
		})
	}

	// Using the total input amount and the transaction's size, compute
	// the sweep and reward amounts. This corresponds to the amount returned
	// to the victim and the amount paid to the tower, respectively. To do
	// so, the required transaction fee is subtracted from the total, and
	// the remaining amount is divided according to the prenegotiated reward
	// rate from the client's session info.
	sweepAmt, rewardAmt, err := p.SessionInfo.ComputeSweepOutputs(
		totalAmt, txSize,
	)
	if err != nil {
		return nil, err
	}

	// TODO(conner): abort/don't add if outputs are dusty

	// Add the sweep and reward outputs to the justice transaction.
	justiceTxn.AddTxOut(&wire.TxOut{
		PkScript: p.JusticeKit.SweepAddress,
		Value:    int64(sweepAmt),
		Version:  txscript.DefaultScriptVersion,
	})
	justiceTxn.AddTxOut(&wire.TxOut{
		PkScript: p.SessionInfo.RewardAddress,
		Value:    int64(rewardAmt),
		Version:  txscript.DefaultScriptVersion,
	})

	// TODO(conner): apply and handle BIP69 sort

	if err := blockchain.CheckTransactionSanity(justiceTxn, p.NetParams); err != nil {
		return nil, err
	}

	// TODO(decred) any others?
	scriptVerifyFlags := txscript.ScriptVerifyCleanStack |
		txscript.ScriptVerifyCheckLockTimeVerify |
		txscript.ScriptVerifyCheckSequenceVerify |
		txscript.ScriptVerifySHA256

	// Attach each of the provided witnesses to the transaction.
	for i, input := range inputs {
		justiceTxn.TxIn[i].SignatureScript, err = lnwallet.WitnessStackToSigScript(
			input.witness,
		)
		if err != nil {
			return nil, err
		}

		// Validate the reconstructed witnesses to ensure they are valid
		// for the breached inputs.
		vm, err := txscript.NewEngine(
			input.txOut.PkScript, justiceTxn, i,
			scriptVerifyFlags,
			input.txOut.Version, nil,
		)
		if err != nil {
			return nil, err
		}
		if err := vm.Execute(); err != nil {
			return nil, err
		}
	}

	return justiceTxn, nil
}

// CreateJusticeTxn computes the justice transaction that sweeps a breaching
// commitment transaction. The justice transaction is constructed by assembling
// the witnesses using data provided by the client in a prior state update.
func (p *JusticeDescriptor) CreateJusticeTxn() (*wire.MsgTx, error) {
	var (
		sweepInputs  = make([]*breachedInput, 0, 2)
		sizeEstimate lnwallet.TxSizeEstimator
	)

	// Add our reward address to the size estimate.
	sizeEstimate.AddP2PKHOutput()

	// Add the sweep address's contribution, depending on whether it is a
	// p2pkh or p2sh output.
	switch int64(len(p.JusticeKit.SweepAddress)) {
	case lnwallet.P2PKHPkScriptSize:
		sizeEstimate.AddP2PKHOutput()

	case lnwallet.P2SHPkScriptSize:
		sizeEstimate.AddP2SHOutput()

	default:
		return nil, ErrUnknownSweepAddrType
	}

	// Assemble the breached to-local output from the justice descriptor and
	// add it to our size estimate.
	toLocalInput, err := p.commitToLocalInput()
	if err != nil {
		return nil, err
	}
	sizeEstimate.AddCustomInput(lnwallet.ToLocalPenaltySigScriptSize)
	sweepInputs = append(sweepInputs, toLocalInput)

	// If the justice kit specifies that we have to sweep the to-remote
	// output, we'll also try to assemble the output and add it to size
	// estimate if successful.
	if p.JusticeKit.HasCommitToRemoteOutput() {
		toRemoteInput, err := p.commitToRemoteInput()
		if err != nil {
			return nil, err
		}
		sizeEstimate.AddP2PKHInput()
		sweepInputs = append(sweepInputs, toRemoteInput)
	}

	// TODO(conner): sweep htlc outputs

	txSize := sizeEstimate.Size()

	return p.assembleJusticeTxn(txSize, sweepInputs...)
}

// findTxOutByPkScript searches the given transaction for an output whose
// pkscript matches the query. If one is found, the TxOut is returned along with
// the index.
//
// NOTE: The search stops after the first match is found.
func findTxOutByPkScript(txn *wire.MsgTx,
	pkScript []byte) (uint32, *wire.TxOut, error) {

	found, index := lnwallet.FindScriptOutputIndex(txn, pkScript)
	if !found {
		return 0, nil, ErrOutputNotFound
	}

	return index, txn.TxOut[index], nil
}

// buildWitness appends the witness script to a given witness stack.
func buildWitness(witnessStack [][]byte, witnessScript []byte) [][]byte {
	witness := make([][]byte, len(witnessStack)+1)
	lastIdx := copy(witness, witnessStack)
	witness[lastIdx] = witnessScript

	return witness
}
