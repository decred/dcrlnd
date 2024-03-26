package channeldb

import (
	"github.com/decred/dcrlnd/build"
	dcrmigration01 "github.com/decred/dcrlnd/channeldb/dcrmigrations/migration01"
	dcrmigration02 "github.com/decred/dcrlnd/channeldb/dcrmigrations/migration02"
	mig "github.com/decred/dcrlnd/channeldb/migration"
	"github.com/decred/dcrlnd/channeldb/migration12"
	"github.com/decred/dcrlnd/channeldb/migration13"
	"github.com/decred/dcrlnd/channeldb/migration16"
	"github.com/decred/dcrlnd/channeldb/migration20"
	"github.com/decred/dcrlnd/channeldb/migration24"
	"github.com/decred/dcrlnd/channeldb/migration_01_to_11"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/slog"
)

// log is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var log slog.Logger

func init() {
	UseLogger(build.NewSubLogger("CHDB", nil))
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
	log = logger
	mig.UseLogger(logger)
	migration_01_to_11.UseLogger(logger)
	migration12.UseLogger(logger)
	migration13.UseLogger(logger)
	migration16.UseLogger(logger)
	migration20.UseLogger(logger)
	migration24.UseLogger(logger)
	kvdb.UseLogger(logger)

	dcrmigration01.UseLogger(logger)
	dcrmigration02.UseLogger(logger)
}
