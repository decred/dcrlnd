// Adapted from the upstream decred/dcrd file contained in the txscript
// package.

package chainscan

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"

	"github.com/decred/dcrd/chaincfg/v3"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
)

const (
	// minSigLen is the minimum length of a signature data push (a
	// DER-encoded ECDSA signature) in a p2pkh sigScript.
	minSigLen = 8

	// maxSigLen is the maximum length of a signature data push (a
	// DER-encoded ECDSA signature) in a p2pkh sigScript.
	maxSigLen = 72

	// compressedPubKeyLen is the length in bytes of a compressed public
	// key.
	compressedPubKeyLen = 33

	// pubKeyHashLen is the length of a P2PKH script.
	pubKeyHashLen = 25

	// scriptHashLen is the length of a P2SH script.
	scriptHashLen = 23

	// maxLen is the maximum script length supported by ParsePkScript.
	maxLen = pubKeyHashLen
)

var (
	// ErrUnsupportedScriptType is an error returned when we attempt to
	// parse/re-compute an output script into a PkScript struct.
	ErrUnsupportedScriptType = errors.New("unsupported script type")
)

// PkScript is a wrapper struct around a byte array, allowing it to be used
// as a map index.
type PkScript struct {
	// class is the type of the script encoded within the byte array. This
	// is used to determine the correct length of the script within the byte
	// array.
	class stdscript.ScriptType

	// script is the script contained within a byte array. If the script is
	// smaller than the length of the byte array, it will be padded with 0s
	// at the end.
	script [maxLen]byte

	// scriptVersion is the script version of the given pkscript. Given
	// this is _not_ embedded in the pkscript itself, it must be provided
	// externally.
	scriptVersion uint16
}

// ParsePkScript parses an output script into the PkScript struct.
// ErrUnsupportedScriptType is returned when attempting to parse an unsupported
// script type.
func ParsePkScript(scriptVersion uint16, pkScript []byte) (PkScript, error) {
	if scriptVersion != 0 {
		return PkScript{}, fmt.Errorf("unsupported script version %d "+
			"(only supports version 0)", scriptVersion)
	}

	outputScript := PkScript{scriptVersion: scriptVersion}
	scriptClass, _ := stdscript.ExtractAddrs(
		scriptVersion, pkScript, chaincfg.MainNetParams(),
	)
	if !isSupportedScriptType(scriptClass) {
		return outputScript, ErrUnsupportedScriptType
	}

	outputScript.class = scriptClass
	copy(outputScript.script[:], pkScript)

	return outputScript, nil
}

// isSupportedScriptType determines whether the script type is supported by the
// PkScript struct.
func isSupportedScriptType(class stdscript.ScriptType) bool {
	switch class {
	case stdscript.STPubKeyHashEcdsaSecp256k1, stdscript.STScriptHash:
		return true
	default:
		return false
	}
}

// Class returns the script type.
func (s PkScript) Class() stdscript.ScriptType {
	return s.class
}

// Script returns the script as a byte slice without any padding. This is a
// copy of the original script, therefore it's safe for modification.
func (s PkScript) Script() []byte {
	var script []byte

	switch s.class {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		script = make([]byte, pubKeyHashLen)
		copy(script, s.script[:pubKeyHashLen])

	case stdscript.STScriptHash:
		script = make([]byte, scriptHashLen)
		copy(script, s.script[:scriptHashLen])

	default:
		// Unsupported script type.
		return nil
	}

	return script
}

// Address encodes the script into an address for the given chain.
func (s PkScript) Address(chainParams *chaincfg.Params) (stdaddr.Address, error) {
	var (
		address stdaddr.Address
		err     error
	)

	switch s.class {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		scriptHash := s.script[3:23]
		address, err = stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(
			scriptHash, chainParams,
		)
	case stdscript.STScriptHash:
		scriptHash := s.script[1:21]
		address, err = stdaddr.NewAddressScriptHashV0FromHash(
			scriptHash, chainParams,
		)
	default:
		err = ErrUnsupportedScriptType
	}

	if err != nil {
		return nil, err
	}
	return address, nil
}

// String returns a hex-encoded string representation of the script.
func (s PkScript) String() string {
	str, _ := txscript.DisasmString(s.Script())
	return str
}

// ScriptVersion returns the recorded script version of the pkscript.
func (s PkScript) ScriptVersion() uint16 {
	return s.scriptVersion
}

// Equal returns true if the other pkscript is equal to this one (has the same
// values).
func (s PkScript) Equal(o *PkScript) bool {
	var slen int

	switch s.class {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		slen = pubKeyHashLen
	case stdscript.STScriptHash:
		slen = scriptHashLen
	default:
		slen = maxLen
	}

	return s.class == o.class &&
		s.scriptVersion == o.scriptVersion &&
		bytes.Equal(s.script[:slen], o.script[:slen])
}

// ComputePkScript computes the pkScript of an transaction output by looking at
// the transaction input's signature script.
//
// NOTE: Only P2PKH and P2SH redeem scripts are supported. Only the standard
// secp256k1 keys are supported (alternative suites are not).
func ComputePkScript(scriptVersion uint16, sigScript []byte) (PkScript, error) {

	var pkScript PkScript

	if scriptVersion != 0 {
		return pkScript, fmt.Errorf("unsupported script version %d "+
			"(only supports version 0)", scriptVersion)
	}

	// Ensure that either an input's signature script or a witness was
	// provided.
	if len(sigScript) == 0 {
		return pkScript, ErrUnsupportedScriptType
	}

	// Create a tokenizer and decode up to the last opcode. Store the first
	// data as well, to check for the correct p2kh sig script style.
	tokenizer := txscript.MakeScriptTokenizer(
		scriptVersion, sigScript,
	)
	var opcodeCount int
	var firstData []byte
	for tokenizer.Next() {
		if tokenizer.Opcode() > txscript.OP_16 {
			return pkScript, ErrUnsupportedScriptType
		}
		if opcodeCount == 0 {
			firstData = tokenizer.Data()
		}
		opcodeCount++
	}
	if tokenizer.Err() != nil {
		return pkScript, tokenizer.Err()
	}

	var scriptClass stdscript.ScriptType
	var script [maxLen]byte

	// The last opcode of a sigscript will either be a pubkey (for p2kh
	// pkscripts) or a redeem script (for p2sh pkscripts). Further, a
	// standard p2pkh will only have an extra signature data push.
	lastData := tokenizer.Data()
	lastDataHash := dcrutil.Hash160(lastData)
	firstDataIsSigLen := len(firstData) >= minSigLen && len(firstData) <= maxSigLen
	lastDataIsPubkeyLen := len(lastData) == compressedPubKeyLen
	if opcodeCount == 2 && firstDataIsSigLen && lastDataIsPubkeyLen {
		// The sigScript has the correct structure for spending a
		// p2pkh, therefore assume it is one.
		scriptClass = stdscript.STPubKeyHashEcdsaSecp256k1
		script = [maxLen]byte{
			0: txscript.OP_DUP,
			1: txscript.OP_HASH160,
			2: txscript.OP_DATA_20,
			// 3-23: pubkey hash
			23: txscript.OP_EQUALVERIFY,
			24: txscript.OP_CHECKSIG,
		}
		copy(script[3:23], lastDataHash)
	} else {
		// Assume it's a p2sh.
		scriptClass = stdscript.STScriptHash
		script = [maxLen]byte{
			0: txscript.OP_HASH160,
			1: txscript.OP_DATA_20,
			// 2-22: script hash
			22: txscript.OP_EQUAL,
		}
		copy(script[2:22], lastDataHash)
	}

	return PkScript{
		class:         scriptClass,
		scriptVersion: scriptVersion,
		script:        script,
	}, nil
}
