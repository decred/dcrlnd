package funding

import (
	"encoding/binary"
	"io"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
)

var (
	// byteOrder defines the endian-ness we use for encoding to and from
	// buffers.
	byteOrder = binary.BigEndian
)

// WriteOutpoint writes an outpoint to an io.Writer. This is not the same as
// the channeldb variant as this uses WriteVarBytes for the Hash.
func WriteOutpoint(w io.Writer, o *wire.OutPoint) error {
	scratch := make([]byte, 4)

	if err := wire.WriteVarBytes(w, 0, o.Hash[:]); err != nil {
		return err
	}

	byteOrder.PutUint32(scratch, o.Index)
	_, err := w.Write(scratch)
	return err
}

const (
	// MinDcrRemoteDelay and maxDcrRemoteDelay are the extremes of the
	// Decred CSV delay we will require the remote to use for its
	// commitment transaction. The actual delay we will require will be
	// somewhere between these values, depending on channel size.
	MinDcrRemoteDelay uint16 = 288
	MaxDcrRemoteDelay uint16 = 4032

	// MinChanFundingSize is the smallest channel that we'll allow to be
	// created over the RPC interface.
	MinChanFundingSize = dcrutil.Amount(20000)

	// maxDecredFundingAmount is a soft-limit of the maximum channel size
	// currently accepted on the Decred chain within the Lightning
	// Protocol. This limit is defined in BOLT-0002, and serves as an
	// initial precautionary limit while implementations are battle tested
	// in the real world.
	MaxDecredFundingAmount = dcrutil.Amount(1<<30) - 1

	// MaxDecredFundingAmountWumbo is a soft-limit on the maximum size of
	// wumbo channels. This limit is 500 DCR and is the only thing standing
	// between you and limitless channel size (apart from 21 million cap)
	MaxDecredFundingAmountWumbo = dcrutil.Amount(500 * 1e8)

	// MaxFundingAmount is a soft-limit of the maximum channel size
	// currently accepted within the Lightning Protocol. This limit is
	// defined in BOLT-0002, and serves as an initial precautionary limit
	// while implementations are battle tested in the real world.
	//
	// TODO(roasbeef): add command line param to modify
	MaxFundingAmount = MaxDecredFundingAmount
)
