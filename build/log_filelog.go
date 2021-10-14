//go:build filelog
// +build filelog

package build

import "os"

var logf *os.File

// LoggingType is a log type that writes to a file.
const LoggingType = LogTypeStdOut

// Write is a noop.
func (w *LogWriter) Write(b []byte) (int, error) {
	return logf.Write(b)
}

func init() {
	var err error
	logf, err = os.Create("dcrlnd.log")
	if err != nil {
		panic(err)
	}
}
