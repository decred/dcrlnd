package lnwire

import (
	"math/big"
	"testing"

	secpv2 "github.com/decred/dcrd/dcrec/secp256k1/v2"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func TestSignatureSerializeDeserialize(t *testing.T) {
	t.Parallel()

	// Local-scoped closure to serialize and deserialize a Signature and
	// check for errors as well as check if the results are correct.
	signatureSerializeDeserialize := func(e secpv2.Signature) error {
		// NOTE(decred): Instead of using NewSigFromSignature we use
		// NewSigFromRawSignature due to performing the test with the
		// v2 secp256k1 lib that allows internal access to R and S and
		// allows creating invalid encoded sigs.
		sig, err := NewSigFromRawSignature(e.Serialize())
		if err != nil {
			return err
		}

		_, err = sig.ToSignature()
		if err != nil {
			return err
		}

		// NOTE(decred) These are commented because the secp256k1/v4
		// doesn't provide access to the internal R and S.
		/*
			if e.R.Cmp(e2.R) != 0 {
				return fmt.Errorf("Pre/post-serialize Rs don't match"+
					": %s, %s", e.R, e2.R)
			}
			if e.S.Cmp(e2.S) != 0 {
				return fmt.Errorf("Pre/post-serialize Ss don't match"+
					": %s, %s", e.S, e2.S)
			}
		*/
		return nil
	}

	sig := secpv2.Signature{}

	// Check R = N-1, S = 128.
	sig.R = big.NewInt(1) // Allocate a big.Int before we call .Sub.
	sig.R.Sub(secp256k1.S256().N, sig.R)
	sig.S = big.NewInt(128)
	err := signatureSerializeDeserialize(sig)
	if err != nil {
		t.Fatalf("R = N-1, S = 128: %s", err.Error())
	}

	// Check R = N-1, S = 127.
	sig.S = big.NewInt(127)
	err = signatureSerializeDeserialize(sig)
	if err != nil {
		t.Fatalf("R = N-1, S = 127: %s", err.Error())
	}

	// Check R = N-1, S = N>>1.
	sig.S.Set(secp256k1.S256().N)
	sig.S.Rsh(sig.S, 1)
	err = signatureSerializeDeserialize(sig)
	if err != nil {
		t.Fatalf("R = N-1, S = N>>1: %s", err.Error())
	}

	// Check R = N-1, S = N.
	sig.S.Set(secp256k1.S256().N)
	err = signatureSerializeDeserialize(sig)
	if err.Error() != "invalid signature: S is 0" {
		t.Fatalf("R = N-1, S = N should become R = N-1, S = 0: %s",
			err.Error())
	}

	// Check R = N-1, S = N-1.
	sig.S.Sub(sig.S, big.NewInt(1))
	// NOTE(decred): this is commented because secp256k1/v4 doesn't provide
	// access to R and S for the comparison to be performed.
	/*
		err = signatureSerializeDeserialize(sig)
		if err.Error() != "Pre/post-serialize Ss don't match: 115792089237316"+
			"195423570985008687907852837564279074904382605163141518161494"+
			"336, 1" {
			t.Fatalf("R = N-1, S = N-1 should become R = N-1, S = 1: %s",
				err.Error())
		}
	*/

	// Check R = 2N, S = 128
	sig.R.Mul(secp256k1.S256().N, big.NewInt(2))
	sig.S.Set(big.NewInt(127))
	err = signatureSerializeDeserialize(sig)
	if err.Error() != "R is over 32 bytes long without padding" {
		t.Fatalf("R = 2N, S = 128, R should be over 32 bytes: %s",
			err.Error())
	}
}
