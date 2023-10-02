package lnwire

import (
	"fmt"

	"github.com/decred/dcrd/dcrutil/v4"
)

const (
	// mAtomScale is a value that's used to scale atoms to milli-atoms, and
	// the other way around.
	mAtomScale uint64 = 1000

	// MaxMilliAtom is the maximum number of msats that can be expressed in
	// this data type.
	MaxMilliAtom = ^MilliAtom(0)
)

// MilliAtom are the native unit of the Lightning Network. A milli-atom
// is simply 1/1000th of an atom. There are 1000 milli-atoms in a single
// atom. Within the network, all HTLC payments are denominated in
// milli-atoms. As milli-atoms aren't deliverable on the native
// blockchain, before settling to broadcasting, the values are rounded down to
// the nearest atom.
type MilliAtom uint64

// NewMAtomsFromAtoms creates a new MilliAtom instance from a target amount
// of Atoms.
func NewMAtomsFromAtoms(at dcrutil.Amount) MilliAtom {
	return MilliAtom(uint64(at) * mAtomScale)
}

// ToCoin converts the target MilliAtom amount to its corresponding value
// when expressed in DCR.
func (mat MilliAtom) ToCoin() float64 {
	at := mat.ToAtoms()
	return at.ToCoin()
}

// ToAtoms converts the target MilliAtom amount to atoms. Simply, this
// sheds a factor of 1000 from the milli-atoms amount in order to convert it to Atoms.
func (mat MilliAtom) ToAtoms() dcrutil.Amount {
	return dcrutil.Amount(uint64(mat) / mAtomScale)
}

// String returns the string representation of the milli-atoms amount.
func (mat MilliAtom) String() string {
	return fmt.Sprintf("%v milli-atoms", uint64(mat))
}

// TODO(roasbeef): extend with arithmetic operations?
