//go:build gofuzz
// +build gofuzz

package zpay32fuzz

import (
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrlnd/zpay32"
)

// Fuzz_decode is used by go-fuzz.
func Fuzz_decode(data []byte) int {
	inv, err := zpay32.Decode(string(data), chaincfg.TestNet3Params())
	if err != nil {
		return 1
	}

	// Call these functions as a sanity check to make sure the invoice
	// is well-formed.
	_ = inv.MinFinalCLTVExpiry()
	_ = inv.Expiry()
	return 1
}
