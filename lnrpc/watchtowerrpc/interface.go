package watchtowerrpc

import (
	"net"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// WatchtowerBackend abstracts access to the watchtower information that is
// served via RPC connections.
type WatchtowerBackend interface {
	// PubKey returns the public key for the watchtower used to
	// authentication and encrypt traffic with clients.
	PubKey() *secp256k1.PublicKey

	// ListeningAddrs returns the listening addresses where the watchtower
	// server can accept client connections.
	ListeningAddrs() []net.Addr

	// ExternalIPs returns the addresses where the watchtower can be reached
	// by clients externally.
	ExternalIPs() []net.Addr
}
