#!/bin/bash

# Simple bash script to build basic lnd tools for all the platforms
# we support with the golang cross-compiler.
#
# Copyright (c) 2016 Company 0, LLC.
# Use of this source code is governed by the ISC
# license.

set -e

LND_VERSION_REGEX="lnd version (.+) commit"
PKG="github.com/lightningnetwork/lnd"
PACKAGE=lnd

# Needed for setting file timestamps to get reproducible archives.
BUILD_DATE="2020-01-01 00:00:00"
BUILD_DATE_STAMP="202001010000.00"

# reproducible_tar_gzip creates a reproducible tar.gz file of a directory. This
# includes setting all file timestamps and ownership settings uniformly.
function reproducible_tar_gzip() {
  local dir=$1
  local tar_cmd=tar

  # MacOS has a version of BSD tar which doesn't support setting the --mtime
  # flag. We need gnu-tar, or gtar for short to be installed for this script to
  # work properly.
  tar_version=$(tar --version)
  if [[ ! "$tar_version" =~ "GNU tar" ]]; then
    if ! command -v "gtar"; then
      echo "GNU tar is required but cannot be found!"
      echo "On MacOS please run 'brew install gnu-tar' to install gtar."
      exit 1
    fi

    # We have gtar installed, use that instead.
    tar_cmd=gtar
  fi

  # Pin down the timestamp time zone.
  export TZ=UTC

  find "${dir}" -print0 | LC_ALL=C sort -r -z | $tar_cmd \
    "--mtime=${BUILD_DATE}" --no-recursion --null --mode=u+rw,go+r-w,a+X \
    --owner=0 --group=0 --numeric-owner -c -T - | gzip -9n > "${dir}.tar.gz"

  rm -r "${dir}"
}

# reproducible_zip creates a reproducible zip file of a directory. This
# includes setting all file timestamps.
function reproducible_zip() {
  local dir=$1

  # Pin down file name encoding and timestamp time zone.
  export TZ=UTC

  # Set the date of each file in the directory that's about to be packaged to
  # the same timestamp and make sure the same permissions are used everywhere.
  chmod -R 0755 "${dir}"
  touch -t "${BUILD_DATE_STAMP}" "${dir}"
  find "${dir}" -print0 | LC_ALL=C sort -r -z | xargs -0r touch \
    -t "${BUILD_DATE_STAMP}"

  find "${dir}" | LC_ALL=C sort -r | zip -o -X -r -@ "${dir}.zip"

  rm -r "${dir}"
}

# green prints one line of green text (if the terminal supports it).
function green() {
  echo -e "\e[0;32m${1}\e[0m"
}

    # Extract version command output.
    LND_VERSION_OUTPUT=`./dcrlnd --version`

    # Use a regex to isolate the version string.
    LND_VERSION_REGEX="dcrlnd version (.+) commit"
    if [[ $LND_VERSION_OUTPUT =~ $LND_VERSION_REGEX ]]; then
        # Prepend 'v' to match git tag naming scheme.
        LND_VERSION="v${BASH_REMATCH[1]}"
        echo "version: $LND_VERSION"

        # If tag contains a release candidate suffix, append this suffix to the
        # lnd reported version before we compare.
        RC_REGEX="-rc[0-9]+$"
        if [[ $TAG =~ $RC_REGEX ]]; then 
            LND_VERSION+=${BASH_REMATCH[0]}
        fi

        # Match git tag with lnd version.
        if [[ $TAG != $LND_VERSION ]]; then
            echo "dcrlnd version $LND_VERSION does not match tag $TAG"
            exit 1
        fi
    else
        echo "malformed dcrlnd version output"
        exit 1
    fi

PACKAGE=dcrlnd
MAINDIR=$PACKAGE-$TAG

mkdir -p releases/$MAINDIR

  green " - Packaging vendor"
  go mod vendor
  reproducible_tar_gzip vendor

  maindir=$PACKAGE-$tag
  mkdir -p $maindir
  mv vendor.tar.gz "${maindir}/"

  # Don't use tag in source directory, otherwise our file names get too long and
  # tar starts to package them non-deterministically.
  package_source="${PACKAGE}-source"

  # The git archive command doesn't support setting timestamps and file
  # permissions. That's why we unpack the tar again, then use our reproducible
  # method to create the final archive.
  git archive -o "${maindir}/${package_source}.tar" HEAD

  cd "${maindir}"
  mkdir -p ${package_source}
  tar -xf "${package_source}.tar" -C ${package_source}
  rm "${package_source}.tar"
  reproducible_tar_gzip ${package_source}
  mv "${package_source}.tar.gz" "${package_source}-$tag.tar.gz" 

# Use the first element of $GOPATH in the case where GOPATH is a list
# (something that is totally allowed).
PKG="github.com/decred/dcrlnd"
COMMIT=$(git describe --abbrev=40 --dirty)
COMMITFLAGS="-X $PKG/build.PreRelease= -X $PKG/build.BuildMetadata=release -X $PKG/build.Commit=$COMMIT"

for i in $SYS; do
    OS=$(echo $i | cut -f1 -d-)
    ARCH=$(echo $i | cut -f2 -d-)
    ARM=

    if [[ $ARCH = "armv6" ]]; then
      ARCH=arm
      ARM=6
    elif [[ $ARCH = "armv7" ]]; then
      ARCH=arm
      ARM=7
    fi

    dir="${PACKAGE}-${i}-${tag}"
    mkdir "${dir}"
    pushd "${dir}"

    echo "Building:" $OS $ARCH $ARM
    env CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH GOARM=$ARM go build -v -trimpath -ldflags "-s -w -buildid= $COMMITFLAGS" github.com/decred/dcrlnd/cmd/dcrlnd
    env CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH GOARM=$ARM go build -v -trimpath -ldflags "-s -w -buildid= $COMMITFLAGS" github.com/decred/dcrlnd/cmd/dcrlncli
    cd ..

    # Add the hashes for the individual binaries as well for easy verification
    # of a single installed binary.
    shasum -a 256 "${dir}/"* >> "manifest-$tag.txt" 

    if [[ $os == "windows" ]]; then
      reproducible_zip "${dir}"
    else
      reproducible_tar_gzip "${dir}"
    fi
  done

  # Add the hash of the packages too, then sort by the second column (name).
  shasum -a 256 lnd-* vendor* >> "manifest-$tag.txt"
  LC_ALL=C sort -k2 -o "manifest-$tag.txt" "manifest-$tag.txt"
  cat "manifest-$tag.txt"
}

# usage prints the usage of the whole script.
function usage() {
  red "Usage: "
  red "release.sh check-tag <version-tag>"
  red "release.sh build-release <version-tag> <build-system(s)> <build-tags> <ldflags>"
}

# Whatever sub command is passed in, we need at least 2 arguments.
if [ "$#" -lt 2 ]; then
  usage
  exit 1
fi

shasum -a 256 * > manifest-$PACKAGE-$TAG.txt
