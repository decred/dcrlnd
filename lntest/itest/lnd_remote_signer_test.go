package itest

import (
	"github.com/decred/dcrlnd/lntest"
)

// testRemoteSigner tests that a watch-only wallet can use a remote signing
// wallet to perform any signing or ECDH operations.
func testRemoteSigner(net *lntest.NetworkHarness, t *harnessTest) {
	t.Skipf("Remote signer is not enabled in dcrlnd")
}
