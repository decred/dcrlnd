package lncfg

// Automation holds some server level automation config options.
type Automation struct {
	// ForceCloseChanReestablishWait is the time after which the automation
	// server force closes a channel where the local peer has sent
	// ChannelReestablish messages but the remote peer does not.
	ForceCloseChanReestablishWait int64 `long:"closechanreestablishwait" description:"Force close a channel if the difference between time channel reestablish msgs were sent and received is higher than the specified one"`
}
