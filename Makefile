PKG := github.com/decred/dcrlnd
ESCPKG := github.com\/decred\/dcrlnd

DCRD_PKG := github.com/decred/dcrd
DCRWALLET_PKG := github.com/decred/dcrwallet
LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
GOACC_PKG := github.com/ory/go-acc
FALAFEL_PKG := github.com/lightninglabs/falafel
GOIMPORTS_PKG := golang.org/x/tools/cmd/goimports
GOFUZZ_BUILD_PKG := github.com/dvyukov/go-fuzz/go-fuzz-build
GOFUZZ_PKG := github.com/dvyukov/go-fuzz/go-fuzz
GOFUZZ_DEP_PKG := github.com/dvyukov/go-fuzz/go-fuzz-dep

GO_BIN := ${GOPATH}/bin
DCRD_BIN := $(GO_BIN)/dcrd
GOMOBILE_BIN := GO111MODULE=off $(GO_BIN)/gomobile
LINT_BIN := $(GO_BIN)/golangci-lint
GOACC_BIN := $(GO_BIN)/go-acc
GOFUZZ_BUILD_BIN := $(GO_BIN)/go-fuzz-build
GOFUZZ_BIN := $(GO_BIN)/go-fuzz

MOBILE_BUILD_DIR :=${GOPATH}/src/$(MOBILE_PKG)/build
IOS_BUILD_DIR := $(MOBILE_BUILD_DIR)/ios
IOS_BUILD := $(IOS_BUILD_DIR)/Lndmobile.xcframework
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

GOACC_COMMIT := 80342ae2e0fcf265e99e76bcc4efd022c7c3811b
LINT_COMMIT := v1.18.0
FALAFEL_COMMIT := v0.7.1
GOFUZZ_COMMIT := b1f3d6f

GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := go test

GOVERSION := $(shell go version | awk '{print $$3}')
GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -name "*pb.go" -not -name "*pb.gw.go" -not -name "*.pb.json.go")

TESTBINPKG := dcrlnd_testbins.tar.gz

RM := rm -f
CP := cp
MAKE := make

include make/testing_flags.mk
include make/release_flags.mk
include make/fuzz_flags.mk

DEV_TAGS := $(if ${tags},$(DEV_TAGS) ${tags},$(DEV_TAGS))

LINT = $(LINT_BIN) run

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
$(LINT_BIN):
	@$(call print, "Installing linter.")
	go install $(LINT_PKG)

$(GOACC_BIN):
	@$(call print, "Installing go-acc.")
	go install $(GOACC_PKG)

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
	go install $(GOIMPORTS_PKG)

$(GOFUZZ_BIN):
	@$(call print, "Installing go-fuzz.")
	go install $(GOFUZZ_PKG)

$(GOFUZZ_BUILD_BIN):
	@$(call print, "Installing go-fuzz-build.")
	go install $(GOFUZZ_BUILD_PKG)

$(GOFUZZ_DEP_BIN):
	@$(call print, "Installing go-fuzz-dep.")
	go install $(GOFUZZ_DEP_PKG)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building debug dcrlnd and dcrlncli.")
	CGO_ENABLED=0 $(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlnd-debug $(LDFLAGS) $(PKG)/cmd/dcrlnd
	CGO_ENABLED=0 $(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlncli-debug $(LDFLAGS) $(PKG)/cmd/dcrlncli

build-itest:
	@$(call print, "Building itest dcrlnd and dcrlncli.")
	CGO_ENABLED=0 $(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlnd-itest$(EXEC_SUFFIX) $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlnd
	CGO_ENABLED=0 $(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlncli-itest$(EXEC_SUFFIX) $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlncli

build-itest-race:
	@$(call print, "Building itest with race detector.")
	CGO_ENABLED=1 $(GOBUILD) -race -tags="$(ITEST_TAGS)" -o lntest/itest/lnd-itest$(EXEC_SUFFIX) $(ITEST_LDFLAGS) $(PKG)/cmd/dcrlnd

	@$(call print, "Building itest binary for ${backend} backend.")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -c -o lntest/itest/itest.test$(EXEC_SUFFIX)

install:
	@$(call print, "Installing dcrlnd and dcrlncli.")
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlncli

release-install:
	@$(call print, "Installing release lnd and lncli.")
	env CGO_ENABLED=0 $(GOINSTALL) -v -trimpath -ldflags="$(RELEASE_LDFLAGS)" -tags="$(RELEASE_TAGS)" $(PKG)/cmd/lnd
	env CGO_ENABLED=0 $(GOINSTALL) -v -trimpath -ldflags="$(RELEASE_LDFLAGS)" -tags="$(RELEASE_TAGS)" $(PKG)/cmd/lncli

release: clean-mobile
	@$(call print, "Releasing dcrlnd and dcrlncli binaries.")
	$(VERSION_CHECK)
	./scripts/release.sh build-release "$(VERSION_TAG)" "$(BUILD_SYSTEM)" "$(RELEASE_TAGS)" "$(RELEASE_LDFLAGS)"

scratch: build


# =======
# TESTING
# =======

check: unit itest

db-instance:
ifeq ($(dbbackend),postgres)
	# Remove a previous postgres instance if it exists.
	docker rm lnd-postgres --force || echo "Starting new postgres container"

	# Start a fresh postgres instance. Allow a maximum of 500 connections so
	# that multiple lnd instances with a maximum number of connections of 50
	# each can run concurrently.
	docker run --name lnd-postgres -e POSTGRES_PASSWORD=postgres -p 6432:5432 -d postgres:13-alpine -N 500
	docker logs -f lnd-postgres &

	# Wait for the instance to be started.
	sleep $(POSTGRES_START_DELAY)
endif


itest-only: db-instance
	@$(call print, "Building itest binary for $(backend) backend")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -c -o lntest/itest/itest.test

	@$(call print, "Running integration tests with ${backend} backend.")
	rm -rf lntest/itest/*.log lntest/itest/.logs-*; date
	EXEC_SUFFIX=$(EXEC_SUFFIX) scripts/itest_part.sh 0 1 $(TEST_FLAGS) $(ITEST_FLAGS)
	lntest/itest/log_check_errors.sh

itest: dcrd dcrwallet build-itest itest-only

itest-parallel-run:
	@$(call print, "Building itest binary for $(backend) backend")
	CGO_ENABLED=0 $(GOTEST) -v ./lntest/itest -tags="$(DEV_TAGS) $(RPC_TAGS) rpctest $(backend)" -c -o lntest/itest/itest.test

	@$(call print, "Running tests")
	rm -rf lntest/itest/*.log lntest/itest/.logs-*
	EXEC_SUFFIX=$(EXEC_SUFFIX) echo "$$(seq 0 $$(expr $(ITEST_PARALLELISM) - 1))" | xargs -P $(ITEST_PARALLELISM) -n 1 -I {} scripts/itest_part.sh {} $(NUM_ITEST_TRANCHES) $(TEST_FLAGS) $(ITEST_FLAGS)


itest-race: build-itest-race itest-only

itest-parallel: dcrd dcrwallet db-instance build-itest itest-parallel-run

itest-clean:
	@$(call print, "Cleaning old itest processes")
	killall dcrlnd-itest || echo "no running dcrlnd-itest process found";

unit-only:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit: dcrd dcrwallet unit-only

unit-debug: dcrd
	@$(call print, "Running debug unit tests.")
	$(UNIT_DEBUG)

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG) -- -test.tags="$(DEV_TAGS) $(LOG_TAGS)"


unit-race:
	@$(call print, "Running unit race tests.")
	$(UNIT_RACE)

ci-race: dcrd dcrwallet unit-race

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
fuzz-build: $(GOFUZZ_BUILD_BIN) $(GOFUZZ_DEP_BIN)
	@$(call print, "Creating fuzz harnesses for packages '$(FUZZPKG)'.")
	scripts/fuzz.sh build "$(FUZZPKG)"

fuzz-run: $(GOFUZZ_BIN)
	@$(call print, "Fuzzing packages '$(FUZZPKG)'.")
	scripts/fuzz.sh run "$(FUZZPKG)" "$(FUZZ_TEST_RUN_TIME)" "$(FUZZ_TEST_TIMEOUT)" "$(FUZZ_NUM_PROCESSES)" "$(FUZZ_BASE_WORKDIR)"

# =========
# UTILITIES
# =========

fmt: goimports
	@$(call print, "Fixing imports.")
	goimports -w $(GOFILES_NOVENDOR)
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
	cd ./lnrpc; ../scripts/check-rest-annotations.sh
	if test -n "$$(git describe --dirty | grep dirty)"; then echo "Protos not properly formatted or not compiled with v3.4.0"; git status; git diff; exit 1; fi

sample-conf-check:
	@$(call print, "Making sure every flag has an example in the sample-dcrlnd.conf file")
	for flag in $$(GO_FLAGS_COMPLETION=1 go run -tags="$(RELEASE_TAGS)" $(PKG)/cmd/dcrlnd -- | grep -v help | cut -c3-); do if ! grep -q $$flag sample-dcrlnd.conf; then echo "Command line flag --$$flag not added to sample-dcrlnd.conf"; exit 1; fi; done

mobile-rpc:
	@$(call print, "Creating mobile RPC from protos (prefix=$(prefix)).")
	cd ./lnrpc; COMPILE_MOBILE=1 SUBSERVER_PREFIX=$(prefix) ./gen_protos_docker.sh

vendor:
	@$(call print, "Re-creating vendor directory.")
	rm -r vendor/; go mod vendor

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

clean-mobile:
	@$(call print, "Cleaning autogenerated mobile RPC stubs.")
	$(RM) -r mobile/build
	$(RM) mobile/*_generated.go

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
	unit-debug \
	unit-cover \
	unit-race \
	falafel \
	ci-race \
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
