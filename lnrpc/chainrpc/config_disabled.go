//go:build no_chainrpc
// +build no_chainrpc

package chainrpc

// Config is empty for non-chainrpc builds.
type Config struct{}
