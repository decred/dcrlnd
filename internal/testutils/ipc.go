package testutils

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// ipcPipePair holds both ends of an IPC pipe used to communicate with dcrd.
type ipcPipePair struct {
	r *os.File
	w *os.File

	// Whether to close the R and/or W ends.
	closeR, closeW bool
}

// close closes the required ends of the pipe and returns the first error.
func (p ipcPipePair) close() error {
	var errR, errW error
	if p.closeR {
		errR = p.r.Close()
	}
	if p.closeW {
		errW = p.w.Close()
	}
	if errR == nil {
		return errW
	}
	return errR
}

// newIPCPipePair creates a new IPC pipe pair.
func newIPCPipePair(closeR, closeW bool) (ipcPipePair, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return ipcPipePair{}, err
	}
	return ipcPipePair{r: r, w: w, closeR: closeR, closeW: closeW}, nil
}

// pipeMessage is a generic interface for dcrd pipe messages.
type pipeMessage interface{}

// boundJSONRPCListenAddrEvent is a pipeMessage that tracks the json RPC
// address of the underlying dcrwallet instance.
type boundJSONRPCListenAddrEvent string

// boundGRPCListenAddrEvent is a pipeMessage that tracks the RPC address of the
// underlying dcrwallet instance.
type boundGRPCListenAddrEvent string

// nextIPCMessage returns the next dcrd IPC message read from the passed
// reading-end pipe.
//
// For unknown messages, this returns an empty pipeMessage instead of an error.
func nextIPCMessage(r io.Reader) (pipeMessage, error) {
	var emptyMsg pipeMessage
	const protocolVersion = 1

	// Bufferize reads from the underlying file.
	r = bufio.NewReader(r)

	// Decode the header.
	var bProto [1]byte
	var bLenType [1]byte
	var bType [255]byte
	var bLenPay [4]byte

	// Enforce the protocol version.
	if _, err := io.ReadFull(r, bProto[:]); err != nil {
		return emptyMsg, fmt.Errorf("unable to read protocol: %v", err)
	}
	gotProtoVersion := bProto[0]
	if gotProtoVersion != protocolVersion {
		return emptyMsg, fmt.Errorf("protocol version mismatch: %d != %d",
			gotProtoVersion, protocolVersion)
	}

	// Decode rest of header.
	if _, err := io.ReadFull(r, bLenType[:]); err != nil {
		return emptyMsg, fmt.Errorf("unable to read type length: %v", err)
	}
	lenType := bLenType[0]
	if _, err := io.ReadFull(r, bType[:lenType]); err != nil {
		return emptyMsg, fmt.Errorf("unable to read type: %v", err)
	}
	if _, err := io.ReadFull(r, bLenPay[:]); err != nil {
		return emptyMsg, fmt.Errorf("unable to read payload length: %v", err)
	}

	// The existing IPC messages are small, so reading the entire message
	// in an in-memory buffer is feasible today.
	lenPay := binary.LittleEndian.Uint32(bLenPay[:])
	payload := make([]byte, lenPay)
	if _, err := io.ReadFull(r, payload); err != nil {
		return emptyMsg, fmt.Errorf("unable to read payload: %v", err)
	}

	// Decode the payload based on the type.
	typ := string(bType[:lenType])
	switch typ {
	case "jsonrpclistener":
		return boundJSONRPCListenAddrEvent(string(payload)), nil
	case "grpclistener":
		return boundGRPCListenAddrEvent(string(payload)), nil
	default:
		// Other message types are unsupported but don't cause a read
		// error.
		return emptyMsg, nil
	}
}
