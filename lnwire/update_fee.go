package lnwire

import (
	"bytes"
	"io"
)

// UpdateFee is the message the channel initiator sends to the other peer if
// the channel commitment fee needs to be updated.
type UpdateFee struct {
	// ChanID is the channel that this UpdateFee is meant for.
	ChanID ChannelID

	// FeePerKB is the fee-per-kB on commit transactions that the sender of
	// this message wants to use for this channel.
	//
	// TODO(halseth): make AtomsPerKWeight when fee estimation is moved to
	// own package. Currently this will cause an import cycle.
	FeePerKB uint32

	// ExtraData is the set of data that was appended to this message to
	// fill out the full maximum transport message size. These fields can
	// be used to specify optional data such as custom TLV fields.
	ExtraData ExtraOpaqueData
}

// NewUpdateFee creates a new UpdateFee message.
func NewUpdateFee(chanID ChannelID, feePerKB uint32) *UpdateFee {
	return &UpdateFee{
		ChanID:   chanID,
		FeePerKB: feePerKB,
	}
}

// A compile time check to ensure UpdateFee implements the lnwire.Message
// interface.
var _ Message = (*UpdateFee)(nil)

// Decode deserializes a serialized UpdateFee message stored in the passed
// io.Reader observing the specified protocol version.
//
// This is part of the lnwire.Message interface.
func (c *UpdateFee) Decode(r io.Reader, pver uint32) error {
	return ReadElements(r,
		&c.ChanID,
		&c.FeePerKB,
		&c.ExtraData,
	)
}

// Encode serializes the target UpdateFee into the passed io.Writer
// observing the protocol version specified.
//
// This is part of the lnwire.Message interface.
func (c *UpdateFee) Encode(w *bytes.Buffer, pver uint32) error {
	return WriteElements(w,
		c.ChanID,
		c.FeePerKB,
		c.ExtraData,
	)
}

// MsgType returns the integer uniquely identifying this message type on the
// wire.
//
// This is part of the lnwire.Message interface.
func (c *UpdateFee) MsgType() MessageType {
	return MsgUpdateFee
}

// TargetChanID returns the channel id of the link for which this message is
// intended.
//
// NOTE: Part of peer.LinkUpdater interface.
func (c *UpdateFee) TargetChanID() ChannelID {
	return c.ChanID
}
