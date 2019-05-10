package dcrwallet

import (
	"github.com/decred/dcrlnd/build"
	"github.com/decred/dcrwallet/chain/v2"
	"github.com/decred/dcrwallet/loader"
	base "github.com/decred/dcrwallet/wallet/v2"
	"github.com/decred/dcrwallet/wallet/v2/udb"
	"github.com/decred/slog"
)

// dcrwLog is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var dcrwLog slog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger("DCRW", nil))
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
	dcrwLog = logger
	base.UseLogger(logger)
	loader.UseLogger(logger)
	chain.UseLogger(logger)
	udb.UseLogger(logger)
}
