//go:build !stdlog && !nolog && !filelog
// +build !stdlog,!nolog,!filelog

package build

import (
	"io"
	"os"
)

// LoggingType is a log type that writes to both stdout and the log rotator, if
// present.
const LoggingType = LogTypeDefault

// Stdout is the writer used to actually output data of the app. By default,
// this is the stdout file.
var Stdout io.Writer = os.Stdout

// Write writes the byte slice to both stdout and the log rotator, if present.
func (w *LogWriter) Write(b []byte) (int, error) {
	Stdout.Write(b)
	if w.RotatorPipe != nil {
		w.RotatorPipe.Write(b)
	}
	return len(b), nil
}
