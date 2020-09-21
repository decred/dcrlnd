#!/bin/bash

# This script builds and runs the fuzzers.
go get -u github.com/dvyukov/go-fuzz/...

# Get the seeds from the repo.
git clone https://github.com/Crypt-iQ/lnd_fuzz_seeds

# Change to the fuzz/lnwire directory.
cd fuzz/lnwire

# Build the fuzzers.
find * -maxdepth 1 -regex '[A-Za-z0-9\-_.]'* -not -name fuzz_utils.go | sed 's/\.go$//1' | xargs -I % sh -c '$GOPATH/bin/go-fuzz-build -func Fuzz_% -o lnwire-%-fuzz.zip github.com/lightningnetwork/lnd/fuzz/lnwire'

# Run the fuzzers with the seeds.

