package cluster

import (
	"github.com/decred/dcrlnd/build"
	"github.com/decred/slog"
)

// Subsystem defines the logging code for this subsystem.
const Subsystem = "CLUS"

// log is a logger that is initialized with the slog.Disabled logger.
//
//nolint:unused
var log slog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger(Subsystem, nil))
}

// DisableLog disables all logging output.
func DisableLog() {
	UseLogger(slog.Disabled)
}

// UseLogger uses a specified Logger to output package logging info.
func UseLogger(logger slog.Logger) {
	log = logger
}
