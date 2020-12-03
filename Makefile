PKG := github.com/decred/dcrlnd
ESCPKG := github.com\/decred\/dcrlnd

DCRD_PKG := github.com/decred/dcrd
DCRWALLET_PKG := github.com/decred/dcrwallet
GOVERALLS_PKG := github.com/mattn/goveralls
LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
GOACC_PKG := github.com/ory/go-acc
FALAFEL_PKG := github.com/lightninglabs/falafel
GOIMPORTS_PKG := golang.org/x/tools/cmd/goimports
GOFUZZ_BUILD_PKG := github.com/dvyukov/go-fuzz/go-fuzz-build
GOFUZZ_PKG := github.com/dvyukov/go-fuzz/go-fuzz

GO_BIN := ${GOPATH}/bin
DCRD_BIN := $(GO_BIN)/dcrd
GOMOBILE_BIN := GO111MODULE=off $(GO_BIN)/gomobile
GOVERALLS_BIN := $(GO_BIN)/goveralls
LINT_BIN := $(GO_BIN)/golangci-lint
GOACC_BIN := $(GO_BIN)/go-acc
GOFUZZ_BUILD_BIN := $(GO_BIN)/go-fuzz-build
GOFUZZ_BIN := $(GO_BIN)/go-fuzz

MOBILE_BUILD_DIR :=${GOPATH}/src/$(MOBILE_PKG)/build
IOS_BUILD_DIR := $(MOBILE_BUILD_DIR)/ios
IOS_BUILD := $(IOS_BUILD_DIR)/Lndmobile.framework
ANDROID_BUILD_DIR := $(MOBILE_BUILD_DIR)/android
ANDROID_BUILD := $(ANDROID_BUILD_DIR)/Lndmobile.aar

WTDIRTY := $(shell git diff-index --quiet HEAD -- || echo "-dirty" || echo "")
COMMIT := $(shell git log -1 --format="%H$(WTDIRTY)")
LDFLAGS := -ldflags "-X $(PKG)/build.Commit=$(COMMIT)"
ITEST_LDFLAGS := -ldflags "-X $(PKG)/build.Commit=$(COMMIT)"

# For the release, we want to remove the symbol table and debug information (-s)
# and omit the DWARF symbol table (-w). Also we clear the build ID.
RELEASE_LDFLAGS := $(call make_ldflags, $(RELEASE_TAGS), -s -w -buildid=)

DCRD_REPO := github.com/decred/dcrd
DCRD_COMMIT := v1.8.0
DCRD_META := "$(DCRD_COMMIT).from-dcrlnd"
DCRD_LDFLAGS := "-X github.com/decred/dcrd/internal/version.BuildMetadata=$(DCRD_META)"
DCRD_TMPDIR := $(shell mktemp -d)

DCRWALLET_REPO := github.com/decred/dcrwallet
DCRWALLET_COMMIT := v3.0.0
DCRWALLET_META := $(DCRWALLET_COMMIT).from-dcrlnd
DCRWALLET_LDFLAGS := "-X decred.org/dcrwallet/version.BuildMetadata=$(DCRWALLET_META)"
DCRWALLET_TMPDIR := $(shell mktemp -d)

GOACC_COMMIT := ddc355013f90fea78d83d3a6c71f1d37ac07ecd5
LINT_COMMIT := v1.18.0
FALAFEL_COMMIT := v0.7.1
GOFUZZ_COMMIT := 21309f307f61

DEPGET := cd /tmp && GO111MODULE=on go get -v
GOBUILD := CGO_ENABLED=0 GO111MODULE=on go build -v
GOINSTALL := CGO_ENABLED=0 GO111MODULE=on go install -v
GOTEST := GO111MODULE=on go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")

TESTBINPKG := dcrlnd_testbins.tar.gz

RM := rm -f
CP := cp
MAKE := make

include make/testing_flags.mk
include make/release_flags.mk
include make/fuzz_flags.mk

DEV_TAGS := $(if ${tags},$(DEV_TAGS) ${tags},$(DEV_TAGS))

LINT = $(LINT_BIN) \
	run \
	--skip-files="mobile\\/.*generated\\.go" \
	--disable-all \
	--enable=gofmt \
	--enable=vet \
	--enable=gosimple \
	--enable=unconvert \
	--enable=ineffassign \
	--enable=unused \
	--deadline=10m

GREEN := "\\033[0;32m"
NC := "\\033[0m"
define print
	echo $(GREEN)$1$(NC)
endef

default: scratch

all: scratch check install

# ============
# DEPENDENCIES
# ============

$(GOVERALLS_BIN):
	@$(call print, "Fetching goveralls.")
	go get -u $(GOVERALLS_PKG)

$(LINT_BIN):
	@$(call print, "Fetching linter")
	$(DEPGET) $(LINT_PKG)@$(LINT_COMMIT)

$(GOACC_BIN):
	@$(call print, "Fetching go-acc")
	$(DEPGET) $(GOACC_PKG)@$(GOACC_COMMIT)

dcrd:
	@$(call print, "Installing dcrd $(DCRD_COMMIT).")
	git clone https://$(DCRD_REPO) $(DCRD_TMPDIR)
	cd $(DCRD_TMPDIR) && \
		git checkout $(DCRD_COMMIT) && \
		GO111MODULE=on go build -o "$$GOPATH/bin/dcrd-dcrlnd" -ldflags $(DCRD_LDFLAGS) .
	rm -rf $(DCRD_TMPDIR)

dcrwallet:
	@$(call print, "Installing dcrwallet $(DCRWALLET_COMMIT).")
	git clone https://$(DCRWALLET_REPO) $(DCRWALLET_TMPDIR)
	cd $(DCRWALLET_TMPDIR) && \
		git checkout $(DCRWALLET_COMMIT) && \
		GO111MODULE=on go build -o "$$GOPATH/bin/dcrwallet-dcrlnd" -ldflags $(DCRWALLET_LDFLAGS) .
	rm -rf $(DCRWALLET_TMPDIR)

falafel:
	@$(call print, "Installing falafel.")
	$(DEPGET) $(FALAFEL_PKG)@$(FALAFEL_COMMIT)

goimports:
	@$(call print, "Installing goimports.")
	$(DEPGET) $(GOIMPORTS_PKG)

$(GOFUZZ_BIN):
	@$(call print, "Fetching go-fuzz")
	$(DEPGET) $(GOFUZZ_PKG)@$(GOFUZZ_COMMIT)

$(GOFUZZ_BUILD_BIN):
	@$(call print, "Fetching go-fuzz-build")
	$(DEPGET) $(GOFUZZ_BUILD_PKG)@$(GOFUZZ_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building debug dcrlnd and dcrlncli.")
	$(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlnd-debug $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlncli-debug $(LDFLAGS) $(PKG)/cmd/dcrlncli

build-itest:
	@$(call print, "Building itest dcrlnd and dcrlncli.")
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlnd-itest $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlncli-itest $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlncli

build-all:
	@$(call print, "Building debug dcrlnd and dcrlncli with all submodules.")
	$(GOBUILD) -o dcrlnd-debug $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -o dcrlncli-debug $(LDFLAGS) $(PKG)/cmd/dcrlncli

build-itest-windows:
	@$(call print, "Building itest lnd and lncli.")
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlnd-itest.exe $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlncli-itest.exe $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlncli

install:
	@$(call print, "Installing dcrlnd and dcrlncli.")
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlncli

release:
	@$(call print, "Releasing dcrlnd and dcrlncli binaries.")
	$(VERSION_CHECK)
	./scripts/release.sh build-release "$(VERSION_TAG)" "$(BUILD_SYSTEM)" "$(RELEASE_TAGS)" "$(RELEASE_LDFLAGS)"

scratch: build


# =======
# TESTING
# =======

check: unit itest

itest-only:
	@$(call print, "Building itest binary for $(backend) backend")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -c -o lntest/itest/itest.test

	@$(call print, "Running integration tests with ${backend} backend.")
	rm -rf lntest/itest/*.log lntest/itest/.logs-*; date
	scripts/itest_part.sh 0 1 $(TEST_FLAGS) $(ITEST_FLAGS)
	lntest/itest/log_check_errors.sh

itest: dcrd dcrwallet build-itest itest-only

itest-parallel-run:
	@$(call print, "Building itest binary for $(backend) backend")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -c -o lntest/itest/itest.test

	@$(call print, "Running tests")
	rm -rf lntest/itest/*.log lntest/itest/.logs-*
	echo "$$(seq 0 $$(expr $(ITEST_PARALLELISM) - 1))" | xargs -P $(ITEST_PARALLELISM) -n 1 -I {} scripts/itest_part.sh {} $(NUM_ITEST_TRANCHES) $(TEST_FLAGS)

itest-parallel: dcrd dcrwallet build-itest itest-parallel-run

itest-windows: dcrd dcrwallet build-itest-windows itest-only

itest-parallel-windows-run:
	@$(call print, "Building itest binary for $(backend) backend")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -logoutput -goroutinedump -c -o lntest/itest/itest.test.exe

	@$(call print, "Running tests")
	EXEC_SUFFIX=".exe" echo "$$(seq 0 $$(expr $(ITEST_PARALLELISM) - 1))" | xargs -P $(ITEST_PARALLELISM) -n 1 -I {} scripts/itest_part.sh {} $(NUM_ITEST_TRANCHES) $(TEST_FLAGS)

itest-parallel-windows: dcrd dcrwallet build-itest-windows itest-parallel-windows-run

unit-only:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit: dcrd dcrwallet unit-only

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG) -- -test.tags="$(DEV_TAGS) $(LOG_TAGS)"


unit-race:
	@$(call print, "Running unit race tests.")
	$(UNIT_RACE)

goveralls: $(GOVERALLS_BIN)
	@$(call print, "Sending coverage report.")
	$(GOVERALLS_BIN) -coverprofile=coverage.txt -service=travis-ci

ci-race: dcrd dcrwallet unit-race

travis-cover: dcrd dcrwallet unit-cover goveralls

ci-itest: itest

package-test-binaries: dcrd dcrwallet build-itest
	@$(call print, "Creating test binaries package $(TESTBINPKG)")
	tar --transform 's/.*\///g' -czf $(TESTBINPKG) $(GO_BIN)/dcrd-dcrlnd $(GO_BIN)/dcrwallet-dcrlnd dcrlnd-itest dcrlncli-itest

unpack-test-binaries:
	@$(call print, "Unpacking test binaries from $(TESTBINPKG)")
	tar -xf $(TESTBINPKG)
	mkdir -p $(GO_BIN)
	mv dcrd-dcrlnd $(GO_BIN)/dcrd-dcrlnd
	mv dcrwallet-dcrlnd $(GO_BIN)/dcrwallet-dcrlnd

# =============
# FLAKE HUNTING
# =============

flakehunter: build-itest
	@$(call print, "Flake hunting ${backend} integration tests.")
	while [ $$? -eq 0 ]; do make itest-only icase='${icase}' backend='${backend}'; done

flake-unit:
	@$(call print, "Flake hunting unit tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(UNIT) -count=1; done

flakehunter-parallel:
	@$(call print, "Flake hunting ${backend} integration tests in parallel.")
	while [ $$? -eq 0 ]; do make itest-parallel tranches=1 parallel=${ITEST_PARALLELISM} icase='${icase}' backend='${backend}'; done

# =============
# FUZZING
# =============
fuzz-build: $(GOFUZZ_BUILD_BIN)
	@$(call print, "Creating fuzz harnesses for packages '$(FUZZPKG)'.")
	scripts/fuzz.sh build "$(FUZZPKG)"

fuzz-run: $(GOFUZZ_BIN)
	@$(call print, "Fuzzing packages '$(FUZZPKG)'.")
	scripts/fuzz.sh run "$(FUZZPKG)" "$(FUZZ_TEST_RUN_TIME)" "$(FUZZ_TEST_TIMEOUT)" "$(FUZZ_NUM_PROCESSES)" "$(FUZZ_BASE_WORKDIR)"

# =========
# UTILITIES
# =========

fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: $(LINT_BIN)
	@$(call print, "Linting source.")
	$(LINT)

list:
	@$(call print, "Listing commands.")
	@$(MAKE) -qp | \
		awk -F':' '/^[a-zA-Z0-9][^$$#\/\t=]*:([^=]|$$)/ {split($$1,A,/ /);for(i in A)print A[i]}' | \
		grep -v Makefile | \
		sort

rpc:
	@$(call print, "Compiling protos.")
	cd ./lnrpc; ./gen_protos.sh

rpc-format:
	@$(call print, "Formatting protos.")
	cd ./lnrpc; find . -name "*.proto" | xargs clang-format --style=file -i

rpc-check: rpc
	@$(call print, "Verifying protos.")
	for rpc in $$(find lnrpc/ -name "*.proto" | $(XARGS) awk '/    rpc /{print $$2}'); do if ! grep -q $$rpc lnrpc/rest-annotations.yaml; then echo "RPC $$rpc not added to lnrpc/rest-annotations.yaml"; exit 1; fi; done
	if test -n "$$(git describe --dirty | grep dirty)"; then echo "Protos not properly formatted or not compiled with v3.4.0"; git status; git diff; exit 1; fi

sample-conf-check:
	@$(call print, "Making sure every flag has an example in the sample-dcrlnd.conf file")
	for flag in $$(GO_FLAGS_COMPLETION=1 go run -tags="$(RELEASE_TAGS)" $(PKG)/cmd/dcrlnd -- | grep -v help | cut -c3-); do if ! grep -q $$flag sample-dcrlnd.conf; then echo "Command line flag --$$flag not added to sample-dcrlnd.conf"; exit 1; fi; done

mobile-rpc: falafel goimports
	@$(call print, "Creating mobile RPC from protos.")
	cd ./mobile; ./gen_bindings.sh $(FALAFEL_COMMIT)

vendor:
	@$(call print, "Re-creating vendor directory.")
	rm -r vendor/; GO111MODULE=on go mod vendor

ios: vendor mobile-rpc
	@$(call print, "Building iOS framework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=ios -tags="mobile $(DEV_TAGS)" $(LDFLAGS) -v -o $(IOS_BUILD) $(MOBILE_PKG)

android: vendor mobile-rpc
	@$(call print, "Building Android library ($(ANDROID_BUILD)).")
	mkdir -p $(ANDROID_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=android -tags="mobile $(DEV_TAGS)" $(LDFLAGS) -v -o $(ANDROID_BUILD) $(MOBILE_PKG)

mobile: ios android

clean:
	@$(call print, "Cleaning source.$(NC)")
	$(RM) ./dcrlnd-debug ./dcrlncli-debug
	$(RM) ./dcrlnd-itest ./dcrlncli-itest


.PHONY: all \
	dcrd \
	default \
	build \
	install \
	scratch \
	check \
	itest-only \
	itest \
	unit \
	unit-cover \
	unit-race \
	falafel \
	goveralls \
	ci-race \
	travis-cover \
	ci-itest \
	flakehunter \
	flake-unit \
	fmt \
	lint \
	list \
	rpc \
	rpc-format \
	rpc-check \
	mobile-rpc \
	vendor \
	ios \
	android \
	mobile \
	package-test-binaries \
	unpack-test-binaries \
	clean
