//go:build !windows
// +build !windows

package main

import (
	"syscall"

	"golang.org/x/term"
)

func readPassword() ([]byte, error) {
	return term.ReadPassword(syscall.Stdin)
}
