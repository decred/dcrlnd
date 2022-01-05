package psbt

import (
	"errors"
	"io"

	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/wire"
)

// This file only provides a shim for upstream (btcsuite/btcutil/psbt)
// functions required to build dcrlnd with psbt support and ease porting
// upstream changes.
//
// PSBT is not actually currently supported in dcrlnd.

type Packet struct {
	UnsignedTx *wire.MsgTx
	Inputs     []PInput
	Outputs    []POutput
}

func (p *Packet) Serialize(w io.Writer) error {
	panic("psbt.Packet.Serialize() not implemented")
}

func New(inputs []*wire.OutPoint,
	outputs []*wire.TxOut, version int32, nLockTime uint32,
	nSequences []uint32) (*Packet, error) {

	panic("psbt.New not implemented")
}

func NewFromRawBytes(r io.Reader, b64 bool) (*Packet, error) {
	panic("psbt.NewFromRawBytes not implemented")
}

func NewFromUnsignedTx(tx *wire.MsgTx) (*Packet, error) {
	panic("psbt.NewFromUnsignedTx not implemented")
}

type POutput struct{}

func MaybeFinalize(p *Packet, inIndex int) (bool, error) {
	panic("psbt.MaybeFinalize not implemented")
}

func MaybeFinalizeAll(p *Packet) error {
	return errors.New("psbt.MaybeFinalizeAll not implemented")
}

func Extract(p *Packet) (*wire.MsgTx, error) {
	return nil, errors.New("psbt.Extract not implemented")
}

type PInput struct {
	WitnessUtxo        *wire.TxOut
	NonWitnessUtxo     *wire.MsgTx
	FinalScriptSig     []byte
	FinalScriptWitness []byte
	Bip32Derivation    []*Bip32Derivation
	SighashType        txscript.SigHashType
	PartialSigs        []*PartialSig
	RedeemScript       []byte
	WitnessScript      []byte
}
