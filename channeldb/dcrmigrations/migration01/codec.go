package dcrmigration01

import (
	"encoding/binary"
	"io"

	"github.com/decred/dcrd/wire"
)

// readMig20Outpoint reads an outpoint that was stored by the migration20.
func readMig20Outpoint(r io.Reader, o *wire.OutPoint) error {
	if _, err := io.ReadFull(r, o.Hash[:]); err != nil {
		return err
	}
	if err := binary.Read(r, byteOrder, &o.Index); err != nil {
		return err
	}

	return nil
}

// writeMig20Outpoint writes an outpoint from the passed writer.
func writeMig20Outpoint(w io.Writer, o *wire.OutPoint) error {
	if _, err := w.Write(o.Hash[:]); err != nil {
		return err
	}
	if err := binary.Write(w, byteOrder, o.Index); err != nil {
		return err
	}

	return nil
}

// writeOkOutpoint writes an outpoint with the correct format to the passed
// writer.
func writeOkOutpoint(w io.Writer, o *wire.OutPoint) error {
	if _, err := w.Write(o.Hash[:]); err != nil {
		return err
	}
	if err := binary.Write(w, byteOrder, o.Index); err != nil {
		return err
	}
	if err := binary.Write(w, byteOrder, o.Tree); err != nil {
		return err
	}

	return nil
}
