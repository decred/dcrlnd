package postgres

import "github.com/decred/slog"

// log is a logger that is initialized as disabled.  This means the package will
// not perform any logging by default until a logger is set.
//
//nolint:unused
var log = slog.Disabled

// UseLogger uses a specified Logger to output package logging info.
func UseLogger(logger slog.Logger) {
	log = logger
}
