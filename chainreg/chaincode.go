package chainreg

// ChainCode is an enum-like structure for keeping track of the chains
// currently supported within lnd.
type ChainCode uint32

const (
	// DecredChain is Decred's chain.
	DecredChain ChainCode = iota
)

// String returns a string representation of the target ChainCode.
func (c ChainCode) String() string {
	switch c {
	case DecredChain:
		return "decred"
	default:
		return "kekcoin"
	}
}
