package chainreg

import (
	"github.com/decred/dcrlnd/build"
	"github.com/decred/slog"
)

var log slog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger("CHRE", nil))
}

// DisableLog disables all logging output.
func DisableLog() {
	UseLogger(slog.Disabled)
}

// UseLogger uses a specified Logger to output package logging info.
func UseLogger(logger slog.Logger) {
	log = logger
}
