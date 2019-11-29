package dcrlnd

import (
	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/keychain"
)

// activeNetParams is a pointer to the parameters specific to the currently
// active decred network.
var activeNetParams = decredTestNetParams

// decredNetParams couples the p2p parameters of a network with the
// corresponding RPC port of a daemon running on the particular network.
type decredNetParams struct {
	*chaincfg.Params
	rpcPort  string
	CoinType uint32
	dcrwPort string
}

// decredTestNetParams contains parameters specific to the 3rd version of the
// test network.
var decredTestNetParams = decredNetParams{
	Params:   chaincfg.TestNet3Params(),
	rpcPort:  "19109",
	CoinType: keychain.CoinTypeTestnet,
	dcrwPort: "19111",
}

// decredMainNetParams contains parameters specific to the current Decred
// mainnet.
var decredMainNetParams = decredNetParams{
	Params:   chaincfg.MainNetParams(),
	rpcPort:  "9109",
	CoinType: keychain.CoinTypeDecred,
	dcrwPort: "9111",
}

// decredSimNetParams contains parameters specific to the simulation test
// network.
var decredSimNetParams = decredNetParams{
	Params:   chaincfg.SimNetParams(),
	rpcPort:  "19556",
	CoinType: keychain.CoinTypeTestnet,
	dcrwPort: "19558",
}

// regTestNetParams contains parameters specific to a local regtest network.
var regTestNetParams = decredNetParams{
	Params:   chaincfg.RegNetParams(),
	rpcPort:  "19334",
	CoinType: keychain.CoinTypeTestnet,
}

// isTestnet tests if the given params correspond to a testnet
// parameter configuration.
func isTestnet(params *decredNetParams) bool {
	switch params.Params.Net {
	case wire.TestNet3:
		return true
	default:
		return false
	}
}
