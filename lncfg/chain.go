package lncfg

import "github.com/decred/dcrlnd/lnwire"

// Chain holds the configuration options for the daemon's chain settings.
type Chain struct {
	ChainDir string `long:"chaindir" description:"The directory to store the chain's data within."`

	Node string `long:"node" description:"The blockchain interface to use." choice:"dcrd" choice:"dcrw"`

	TestNet3 bool `long:"testnet" description:"Use the test network"`
	SimNet   bool `long:"simnet" description:"Use the simulation test network"`
	RegTest  bool `long:"regtest" description:"Use the regression test network"`

	DefaultNumChanConfs int              `long:"defaultchanconfs" description:"The default number of confirmations a channel must have before it's considered open. If this is not set, we will scale the value according to the channel size."`
	DefaultRemoteDelay  int              `long:"defaultremotedelay" description:"The default number of blocks we will require our channel counterparty to wait before accessing its funds in case of unilateral close. If this is not set, we will scale the value according to the channel size."`
	MinHTLCIn           lnwire.MilliAtom `long:"minhtlc" description:"The smallest HTLC we are willing to accept on our channels, in milliatoms"`
	MinHTLCOut          lnwire.MilliAtom `long:"minhtlcout" description:"The smallest HTLC we are willing to send out on our channels, in milliatoms"`
	BaseFee             lnwire.MilliAtom `long:"basefee" description:"The base fee in milliatoms we will charge for forwarding payments on our channels"`
	FeeRate             lnwire.MilliAtom `long:"feerate" description:"The fee rate used when forwarding payments on our channels. The total fee charged is basefee + (amount * feerate / 1000000), where amount is the forwarded amount."`
	TimeLockDelta       uint32           `long:"timelockdelta" description:"The CLTV delta we will subtract from a forwarded HTLC's timelock value"`
}
