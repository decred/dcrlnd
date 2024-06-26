package lncfg

// ProtocolOptions is a struct that we use to be able to test backwards
// compatibility of protocol additions, while defaulting to the latest within
// lnd, or to enable experimental protocol changes.
type ProtocolOptions struct {
	// LegacyProtocol is a sub-config that houses all the legacy protocol
	// options.  These are mostly used for integration tests as most modern
	// nodes shuld always run with them on by default.
	LegacyProtocol `group:"legacy" namespace:"legacy"`

	// ExperimentalProtocol is a sub-config that houses any experimental
	// protocol features that also require a build-tag to activate.
	ExperimentalProtocol

	// WumboChans should be set if we want to enable support for wumbo
	// (channels larger than 10.7 DCR) channels, which is the opposite of
	// mini.
	WumboChans bool `long:"wumbo-channels" description:"if set, then lnd will create and accept requests for channels larger chan 10.7 DCR"`

	// Anchors enables anchor commitments.
	Anchors bool `long:"anchors" description:"EXPERIMENTAL: some features of anchor channels don't work on decred's mainnet"`

	// ScriptEnforcedLease should be set if we don't want to support
	// opening or accepting channels having the script enforced commitment
	// type for leased channel.
	ScriptEnforcedLease bool `long:"script-enforced-lease" description:"disable support for script enforced lease commitments"`
}

// Wumbo returns true if lnd should permit the creation and acceptance of wumbo
// channels.
func (l *ProtocolOptions) Wumbo() bool {
	return l.WumboChans
}

// NoAnchorCommitments returns true if we have disabled support for the anchor
// commitment type.
func (l *ProtocolOptions) NoAnchorCommitments() bool {
	return !l.Anchors
}

// ScriptEnforcementLease returns true if we have disabled support for the
// script enforcement commitment type for leased channels.
func (l *ProtocolOptions) ScriptEnforcementLease() bool {
	return l.ScriptEnforcedLease
}
