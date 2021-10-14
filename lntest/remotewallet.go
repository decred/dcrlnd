//go:build remotewallet
// +build remotewallet

package lntest

func useRemoteWallet() bool {
	return true
}

func useDcrwNode() bool {
	return true
}
