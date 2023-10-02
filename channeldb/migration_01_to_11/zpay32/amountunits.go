package zpay32

import (
	"fmt"
	"strconv"

	lnwire "github.com/decred/dcrlnd/channeldb/migration/lnwire21"
)

var (
	// toMAtoms is a map from a unit to a function that converts an amount
	// of that unit to MilliAtoms.
	toMAtoms = map[byte]func(uint64) (lnwire.MilliAtom, error){
		'm': mDcrToMAtoms,
		'u': uDcrToMAtoms,
		'n': nDcrToMAtoms,
		'p': pDcrToMAtoms,
	}
)

// mDcrToMAtoms converts the given amount in milliDCR to MilliAtoms.
func mDcrToMAtoms(m uint64) (lnwire.MilliAtom, error) {
	return lnwire.MilliAtom(m) * 100000000, nil
}

// uDcrToMAtoms converts the given amount in microDCR to MilliAtoms.
func uDcrToMAtoms(u uint64) (lnwire.MilliAtom, error) {
	return lnwire.MilliAtom(u * 100000), nil
}

// nDcrToMAtoms converts the given amount in nanoDCR to MilliAtoms.
func nDcrToMAtoms(n uint64) (lnwire.MilliAtom, error) {
	return lnwire.MilliAtom(n * 100), nil
}

// pDcrToMAtoms converts the given amount in picoDCR to MilliAtoms.
func pDcrToMAtoms(p uint64) (lnwire.MilliAtom, error) {
	if p < 10 {
		return 0, fmt.Errorf("minimum amount is 10p")
	}
	if p%10 != 0 {
		return 0, fmt.Errorf("amount %d pDCR not expressible in mAt",
			p)
	}
	return lnwire.MilliAtom(p / 10), nil
}

// decodeAmount returns the amount encoded by the provided string in MilliAtom.
func decodeAmount(amount string) (lnwire.MilliAtom, error) {
	if len(amount) < 1 {
		return 0, fmt.Errorf("amount must be non-empty")
	}

	// If last character is a digit, then the amount can just be
	// interpreted as DCR.
	char := amount[len(amount)-1]
	digit := char - '0'
	if digit <= 9 {
		dcr, err := strconv.ParseUint(amount, 10, 64)
		if err != nil {
			return 0, err
		}
		return lnwire.MilliAtom(dcr) * mAtPerDcr, nil
	}

	// If not a digit, it must be part of the known units.
	conv, ok := toMAtoms[char]
	if !ok {
		return 0, fmt.Errorf("unknown multiplier %c", char)
	}

	// Known unit.
	num := amount[:len(amount)-1]
	if len(num) < 1 {
		return 0, fmt.Errorf("number must be non-empty")
	}

	am, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, err
	}

	return conv(am)
}
