package kvdb

import (
	"github.com/decred/dcrlnd/kvdb/postgres"
	"github.com/decred/slog"
)

// log is a logger that is initialized as disabled.  This means the package will
// not perform any logging by default until a logger is set.
var log = slog.Disabled

// UseLogger uses a specified Logger to output package logging info.
func UseLogger(logger slog.Logger) {
	log = logger

	postgres.UseLogger(log)
}
