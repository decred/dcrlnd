package lnwire

import (
	"io"

	"github.com/decred/dcrd/dcrutil/v4"
)

// ClosingSigned is sent by both parties to a channel once the channel is clear
// of HTLCs, and is primarily concerned with negotiating fees for the close
// transaction. Each party provides a signature for a transaction with a fee
// that they believe is fair. The process terminates when both sides agree on
// the same fee, or when one side force closes the channel.
//
// NOTE: The responder is able to send a signature without any additional
// messages as all transactions are assembled observing BIP 69 which defines a
// canonical ordering for input/outputs. Therefore, both sides are able to
// arrive at an identical closure transaction as they know the order of the
// inputs/outputs.
type ClosingSigned struct {
	// ChannelID serves to identify which channel is to be closed.
	ChannelID ChannelID

	// FeeAtoms is the total fee in atoms that the party to the
	// channel would like to propose for the close transaction.
	FeeAtoms dcrutil.Amount

	// Signature is for the proposed channel close transaction.
	Signature Sig

	// ExtraData is the set of data that was appended to this message to
	// fill out the full maximum transport message size. These fields can
	// be used to specify optional data such as custom TLV fields.
	ExtraData ExtraOpaqueData
}

// NewClosingSigned creates a new empty ClosingSigned message.
func NewClosingSigned(cid ChannelID, fa dcrutil.Amount,
	sig Sig) *ClosingSigned {

	return &ClosingSigned{
		ChannelID: cid,
		FeeAtoms:  fa,
		Signature: sig,
	}
}

// A compile time check to ensure ClosingSigned implements the lnwire.Message
// interface.
var _ Message = (*ClosingSigned)(nil)

// Decode deserializes a serialized ClosingSigned message stored in the passed
// io.Reader observing the specified protocol version.
//
// This is part of the lnwire.Message interface.
func (c *ClosingSigned) Decode(r io.Reader, pver uint32) error {
	return ReadElements(
		r, &c.ChannelID, &c.FeeAtoms, &c.Signature, &c.ExtraData,
	)
}

// Encode serializes the target ClosingSigned into the passed io.Writer
// observing the protocol version specified.
//
// This is part of the lnwire.Message interface.
func (c *ClosingSigned) Encode(w io.Writer, pver uint32) error {
	return WriteElements(
		w, c.ChannelID, c.FeeAtoms, c.Signature, c.ExtraData,
	)
}

// MsgType returns the integer uniquely identifying this message type on the
// wire.
//
// This is part of the lnwire.Message interface.
func (c *ClosingSigned) MsgType() MessageType {
	return MsgClosingSigned
}

// MaxPayloadLength returns the maximum allowed payload size for a
// ClosingSigned complete message observing the specified protocol version.
//
// This is part of the lnwire.Message interface.
func (c *ClosingSigned) MaxPayloadLength(uint32) uint32 {
	return MaxMsgBody
}
