package lnwallet

import "strings"

// ReasonsChannelUnclean returns a string with the reason(s) the channel is not
// clean or an empty string if it is clean.
func (lc *LightningChannel) ReasonsChannelUnclean() string {
	lc.RLock()
	defer lc.RUnlock()

	var reasons []string

	// Check whether we have a pending commitment for our local state.
	if lc.localCommitChain.hasUnackedCommitment() {
		reasons = append(reasons, "localChainHasUnackedCommit")
	}

	// Check whether our counterparty has a pending commitment for their
	// state.
	if lc.remoteCommitChain.hasUnackedCommitment() {
		reasons = append(reasons, "remoteChainHasUnackedCommit")
	}

	// We call ActiveHtlcs to ensure there are no HTLCs on either
	// commitment.
	if len(lc.channelState.ActiveHtlcs()) != 0 {
		reasons = append(reasons, "hasActiveHtlcs")
	}

	// Now check that both local and remote commitments are signing the
	// same updates.
	if lc.oweCommitment(true) {
		reasons = append(reasons, "owedLocalCommit")
	}

	if lc.oweCommitment(false) {
		reasons = append(reasons, "owesRemoteCommit")
	}

	// If we reached this point, the channel has no HTLCs and both
	// commitments sign the same updates.
	return strings.Join(reasons, ",")

}
