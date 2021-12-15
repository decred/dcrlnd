package chanacceptor

import (
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrlnd/lnwire"
)

// ChannelAcceptRequest is a struct containing the requesting node's public key
// along with the lnwire.OpenChannel message that they sent when requesting an
// inbound channel. This information is provided to each acceptor so that they
// can each leverage their own decision-making with this information.
type ChannelAcceptRequest struct {
	// Node is the public key of the node requesting to open a channel.
	Node *secp256k1.PublicKey

	// OpenChanMsg is the actual OpenChannel protocol message that the peer
	// sent to us.
	OpenChanMsg *lnwire.OpenChannel
}

// ChannelAcceptor is an interface that represents a predicate on the data
// contained in ChannelAcceptRequest.
type ChannelAcceptor interface {
	Accept(req *ChannelAcceptRequest) bool
}
