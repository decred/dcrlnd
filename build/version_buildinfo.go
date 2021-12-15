// Copyright (c) 2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

//go:build go1.18
// +build go1.18

package build

import (
	"runtime/debug"
)

func vcsCommitID() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var vcs, revision, dirty string
	for _, bs := range bi.Settings {
		switch bs.Key {
		case "vcs":
			vcs = bs.Value
		case "vcs.revision":
			revision = bs.Value
		case "vcs.modified":
			if bs.Value == "true" {
				dirty = ".dirty"
			}
		}
	}
	if vcs == "" {
		return ""
	}
	if vcs == "git" && len(revision) > 9 {
		revision = revision[:9]
	}
	return revision + dirty
}
