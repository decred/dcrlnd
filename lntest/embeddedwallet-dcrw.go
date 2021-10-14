//go:build embeddedwallet_dcrw
// +build embeddedwallet_dcrw

package lntest

func useRemoteWallet() bool {
	return false
}

func useDcrwNode() bool {
	return true
}
