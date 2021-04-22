package keychain

import (
	"github.com/decred/dcrlnd/build"
	"github.com/decred/slog"
)

// keychainLog is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var keychainLog slog.Logger // nolint: unused

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger("KCHN", nil))
}

// DisableLog disables all library log output.  Logging output is disabled
// by default until UseLogger is called.
func DisableLog() {
	UseLogger(slog.Disabled)
}

// UseLogger uses a specified Logger to output package logging info.
// This should be used in preference to SetLogWriter if the caller is also
// using slog.
func UseLogger(logger slog.Logger) {
	keychainLog = logger
}
