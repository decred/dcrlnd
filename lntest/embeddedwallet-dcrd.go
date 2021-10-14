//go:build !remotewallet && !embeddedwallet_dcrw
// +build !remotewallet,!embeddedwallet_dcrw

package lntest

func useRemoteWallet() bool {
	return false
}

func useDcrwNode() bool {
	return false
}
