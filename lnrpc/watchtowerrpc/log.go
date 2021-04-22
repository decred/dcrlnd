package watchtowerrpc

import (
	"github.com/decred/dcrlnd/build"
	"github.com/decred/slog"
)

// Subsystem defines the logging code for this subsystem.
const Subsystem = "WRPC"

// log is a logger that is initialized with no output filters. This means the
// package will not perform any logging by default until the caller requests it.
var log slog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger(Subsystem, nil))
}

// DisableLog disables all library log output. Logging output is disabled by by
// default until UseLogger is called.
func DisableLog() {
	UseLogger(slog.Disabled)
}

// UseLogger uses a specified Logger to output package logging info. This
// should be used in preference to SetLogWriter if the caller is also using
// slog.
func UseLogger(logger slog.Logger) {
	log = logger
}
