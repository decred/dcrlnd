package lncfg

import (
	"fmt"

	"github.com/decred/dcrlnd/lnwire"
)

// Chain holds the configuration options for the daemon's chain settings.
type Chain struct {
	ChainDir string `long:"chaindir" description:"The directory to store the chain's data within."`

	Node string `long:"node" description:"The blockchain interface to use." choice:"dcrd" choice:"dcrw" choice:"nochainbackend"`

	TestNet3 bool `long:"testnet" description:"Use the test network"`
	SimNet   bool `long:"simnet" description:"Use the simulation test network"`
	RegTest  bool `long:"regtest" description:"Use the regression test network"`

	DefaultNumChanConfs int              `long:"defaultchanconfs" description:"The default number of confirmations a channel must have before it's considered open. If this is not set, we will scale the value according to the channel size."`
	DefaultRemoteDelay  int              `long:"defaultremotedelay" description:"The default number of blocks we will require our channel counterparty to wait before accessing its funds in case of unilateral close. If this is not set, we will scale the value according to the channel size."`
	MaxLocalDelay       uint16           `long:"maxlocaldelay" description:"The maximum blocks we will allow our funds to be timelocked before accessing its funds in case of unilateral close. If a peer proposes a value greater than this, we will reject the channel."`
	MinHTLCIn           lnwire.MilliAtom `long:"minhtlc" description:"The smallest HTLC we are willing to accept on our channels, in milliatoms"`
	MinHTLCOut          lnwire.MilliAtom `long:"minhtlcout" description:"The smallest HTLC we are willing to send out on our channels, in milliatoms"`
	BaseFee             lnwire.MilliAtom `long:"basefee" description:"The base fee in milliatoms we will charge for forwarding payments on our channels"`
	FeeRate             lnwire.MilliAtom `long:"feerate" description:"The fee rate used when forwarding payments on our channels. The total fee charged is basefee + (amount * feerate / 1000000), where amount is the forwarded amount."`
	TimeLockDelta       uint32           `long:"timelockdelta" description:"The CLTV delta we will subtract from a forwarded HTLC's timelock value"`
	DNSSeeds            []string         `long:"dnsseed" description:"The seed DNS server(s) to use for initial peer discovery. Must be specified as a '<primary_dns>[,<soa_primary_dns>]' tuple where the SOA address is needed for DNS resolution through Tor but is optional for clearnet users. Multiple tuples can be specified, will overwrite the default seed servers."`
}

// Validate performs validation on our chain config.
func (c *Chain) Validate(minTimeLockDelta uint32, minDelay uint16) error {
	if c.TimeLockDelta < minTimeLockDelta {
		return fmt.Errorf("timelockdelta must be at least %v",
			minTimeLockDelta)
	}

	// Check that our max local delay isn't set below some reasonable
	// minimum value. We do this to prevent setting an unreasonably low
	// delay, which would mean that the node would accept no channels.
	if c.MaxLocalDelay < minDelay {
		return fmt.Errorf("MaxLocalDelay must be at least: %v",
			minDelay)
	}

	return nil
}
