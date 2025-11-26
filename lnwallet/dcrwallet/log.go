package dcrwallet

import (
	"decred.org/dcrwallet/v5/chain"
	"decred.org/dcrwallet/v5/p2p"
	"decred.org/dcrwallet/v5/spv"
	base "decred.org/dcrwallet/v5/wallet"
	"decred.org/dcrwallet/v5/wallet/udb"
	"github.com/decred/dcrd/addrmgr/v3"
	"github.com/decred/dcrlnd/build"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
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
	spv.UseLogger(logger)
	p2p.UseLogger(logger)
	udb.UseLogger(logger)
	addrmgr.UseLogger(logger)
}
