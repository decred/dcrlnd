package chainreg

import (
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/keychain"
)

// DecredNetParams couples the p2p parameters of a network with the
// corresponding RPC port of a daemon running on the particular network.
type DecredNetParams struct {
	*chaincfg.Params
	rpcPort  string
	CoinType uint32
	dcrwPort string
}

// decredTestNetParams contains parameters specific to the 3rd version of the
// test network.
var DecredTestNetParams = DecredNetParams{
	Params:   chaincfg.TestNet3Params(),
	rpcPort:  "19109",
	CoinType: keychain.CoinTypeTestnet,
	dcrwPort: "19111",
}

// DecredMainNetParams contains parameters specific to the current Decred
// mainnet.
var DecredMainNetParams = DecredNetParams{
	Params:   chaincfg.MainNetParams(),
	rpcPort:  "9109",
	CoinType: keychain.CoinTypeDecred,
	dcrwPort: "9111",
}

// decredSimNetParams contains parameters specific to the simulation test
// network.
var DecredSimNetParams = DecredNetParams{
	Params:   chaincfg.SimNetParams(),
	rpcPort:  "19556",
	CoinType: keychain.CoinTypeTestnet,
	dcrwPort: "19558",
}

// regTestNetParams contains parameters specific to a local regtest network.
var RegTestNetParams = DecredNetParams{
	Params:   chaincfg.RegNetParams(),
	rpcPort:  "19334",
	CoinType: keychain.CoinTypeTestnet,
}

// IsTestnet tests if the given params correspond to a testnet
// parameter configuration.
func IsTestnet(params *DecredNetParams) bool {
	switch params.Params.Net {
	case wire.TestNet3:
		return true
	default:
		return false
	}
}
