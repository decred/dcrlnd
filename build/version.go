// Copyright (c) 2013-2014 The btcsuite developers
// Copyright (c) 2015-2018 The Decred developers
// Copyright (c) 2015-2017 The Lightning Network Developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package build

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	// semanticAlphabet defines the allowed characters for the pre-release
	// portion of a semantic version string.
	semanticAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-"

	// semanticBuildAlphabet defines the allowed characters for the build
	// portion of a semantic version string.
	semanticBuildAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-."
)

// These constants define the application version and follow the semantic
// versioning 2.0.0 spec (https://semver.org/).
const (
	appMajor uint = 0
	appMinor uint = 2
	appPatch uint = 1
)

var (
	// PreRelease is defined as a variable so it can be overridden during the
	// build process with:
	// '-ldflags "-X github.com/decred/dcrlnd/build.PreRelease=foo"'
	// if needed.  It MUST only contain characters from semanticAlphabet per
	// the semantic versioning spec.
	PreRelease = "pre"

	// BuildMetadata is defined as a variable so it can be overridden during the
	// build process with:
	// '-ldflags "-X github.com/decred/dcrlnd/build.BuildMetadata=foo"'
	// if needed.  It MUST only contain characters from semanticBuildAlphabet
	// per the semantic versioning spec.
	BuildMetadata = "dev"

	// Commit is defined as a variable so it can be overriden during the
	// build process with:
	// '-ldflags "-X github.com/decred/dcrlnd/build.Commit=foo"'
	// if needed. It is NOT included in the version string returned by this
	// module and MUST only contain characters from semanticBuildAlphabet.
	Commit = ""
)

// Version returns the application version as a properly formed string per the
// semantic versioning 2.0.0 spec (https://semver.org/).
func Version() string {
	// Start with the major, minor, and patch versions.
	version := fmt.Sprintf("%d.%d.%d", appMajor, appMinor, appPatch)

	// Append pre-release version if there is one.  The hyphen called for
	// by the semantic versioning spec is automatically appended and should
	// not be contained in the pre-release string.  The pre-release version
	// is not appended if it contains invalid characters.
	preRelease := normalizePreRelString(PreRelease)
	if preRelease != "" {
		version = fmt.Sprintf("%s-%s", version, preRelease)
	}

	// Append build metadata if there is any.  The plus called for
	// by the semantic versioning spec is automatically appended and should
	// not be contained in the build metadata string.  The build metadata
	// string is not appended if it contains invalid characters.
	build := normalizeBuildString(BuildMetadata)
	if build != "" {
		version = fmt.Sprintf("%s+%s", version, build)
	}

	return version
}

// SourceCommit returns the normalized version of the Commit variable according
// to the rules of semantic versioning guidelines.
func SourceCommit() string {
	return normalizeBuildString(Commit)
}

// normalizeSemString returns the passed string stripped of all characters
// which are not valid according to the provided semantic versioning alphabet.
func normalizeSemString(str, alphabet string) string {
	var result bytes.Buffer
	for _, r := range str {
		if strings.ContainsRune(alphabet, r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// normalizePreRelString returns the passed string stripped of all characters
// which are not valid according to the semantic versioning guidelines for
// pre-release strings.  In particular they MUST only contain characters in
// semanticAlphabet.
func normalizePreRelString(str string) string {
	return normalizeSemString(str, semanticAlphabet)
}

// normalizeBuildString returns the passed string stripped of all characters
// which are not valid according to the semantic versioning guidelines for
// build metadata strings.  In particular they MUST only contain characters in
// semanticBuildAlphabet.
func normalizeBuildString(str string) string {
	return normalizeSemString(str, semanticBuildAlphabet)
}
