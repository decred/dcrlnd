# This is currently changed to use the local dir (.) as package instead of
# github.com/decred/dcrlnd so that makefile actions (such as running integration
# tests) are executed against the local dir (possibly with local changes)
# instead of trying to execute them against the official repo module's.
#PKG := github.com/decred/dcrlnd
PKG := .
FULLPKG := github.com/decred/dcrlnd
ESCPKG := github.com\/decred\/dcrlnd

DCRD_PKG := github.com/decred/dcrd
DCRWALLET_PKG := github.com/decred/dcrwallet
GOVERALLS_PKG := github.com/mattn/goveralls
LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
GOACC_PKG := github.com/ory/go-acc

GO_BIN := ${GOPATH}/bin
DCRD_BIN := $(GO_BIN)/dcrd
GOMOBILE_BIN := GO111MODULE=off $(GO_BIN)/gomobile
GOVERALLS_BIN := $(GO_BIN)/goveralls
LINT_BIN := $(GO_BIN)/golangci-lint
GOACC_BIN := $(GO_BIN)/go-acc

MOBILE_BUILD_DIR :=${GOPATH}/src/$(MOBILE_PKG)/build
IOS_BUILD_DIR := $(MOBILE_BUILD_DIR)/ios
IOS_BUILD := $(IOS_BUILD_DIR)/Lndmobile.framework
ANDROID_BUILD_DIR := $(MOBILE_BUILD_DIR)/android
ANDROID_BUILD := $(ANDROID_BUILD_DIR)/Lndmobile.aar

COMMIT := $(shell git describe --abbrev=40 --dirty)
LDFLAGS := -ldflags "-X $(FULLPKG)/build.Commit=$(COMMIT)"

DCRD_COMMIT := $(shell cat go.mod | \
		grep $(DCRD_PKG) | \
		head -n1 | \
		awk -F " " '{ print $$2 }' | \
		awk -F "/" '{ print $$1 }' | \
		awk -F "-" '{ print $$NF }' )
DCRD_META := "$(DCRD_COMMIT).from-dcrlnd"
DCRD_LDFLAGS := "-X github.com/decred/dcrd/internal/version.BuildMetadata=$(DCRD_META)"
DCRD_TMPDIR := $(shell mktemp -d)

DCRWALLET_COMMIT := $(shell cat go.mod | \
		grep $(DCRWALLET_PKG) | \
		head -n1 | \
		awk -F " " '{ print $$2 }' | \
		awk -F "/" '{ print $$1 }' | \
		awk -F "-" '{ print $$NF }' )
DCRWALLET_META := "$(DCRWALLET_COMMIT).from-dcrlnd"
DCRWALLET_LDFLAGS := "-X github.com/decred/dcrwallet/version.BuildMetadata=$(DCRWALLET_META)"
DCRWALLET_TMPDIR := $(shell mktemp -d)

GOACC_COMMIT := ddc355013f90fea78d83d3a6c71f1d37ac07ecd5
LINT_COMMIT := v1.18.0

DEPGET := cd /tmp && GO111MODULE=on go get -v
GOBUILD := GO111MODULE=on go build -v
GOINSTALL := GO111MODULE=on go install -v
GOTEST := GO111MODULE=on go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")
GOLIST := go list $(PKG)/... | grep -v '/vendor/'
GOLISTCOVER := $(shell go list -f '{{.ImportPath}}' ./... | sed -e 's/^$(ESCPKG)/./')

ALL_TAGS="autopilotrpc chainrpc invoicesrpc routerrpc signrpc walletrpc watchtowerrpc wtclientrpc"

RM := rm -f
CP := cp
MAKE := make
XARGS := xargs -L 1

include make/testing_flags.mk

DEV_TAGS := $(if ${tags},$(DEV_TAGS) ${tags},$(DEV_TAGS))

LINT = $(LINT_BIN) \
	run \
	-v \
	--build-tags=$(ALL_TAGS) \
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
	git clone https://github.com/decred/dcrd $(DCRD_TMPDIR)
	cd $(DCRD_TMPDIR) && \
		git checkout $(DCRD_COMMIT) && \
		GO111MODULE=on go install -ldflags $(DCRD_LDFLAGS) . && \
		GO111MODULE=on go install -ldflags $(DCRD_LDFLAGS) ./cmd/dcrctl
	rm -rf $(DCRD_TMPDIR)

dcrwallet:
	@$(call print, "Installing dcrwallet $(DCRWALLET_COMMIT).")
	git clone https://github.com/decred/dcrwallet $(DCRWALLET_TMPDIR)
	cd $(DCRWALLET_TMPDIR) && \
		git checkout $(DCRWALLET_COMMIT) && \
		GO111MODULE=on go build -o "$$GOPATH/bin/dcrwallet-dcrlnd" -ldflags $(DCRWALLET_LDFLAGS) .
	rm -rf $(DCRWALLET_TMPDIR)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building debug dcrlnd and dcrlncli.")
	$(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlnd-debug $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags="$(DEV_TAGS)" -o dcrlncli-debug $(LDFLAGS) $(PKG)/cmd/dcrlncli

build-itest:
	@$(call print, "Building itest dcrlnd and dcrlncli.")
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlnd-itest $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o dcrlncli-itest $(LDFLAGS) $(PKG)/cmd/dcrlncli

build-all: 
	@$(call print, "Building debug dcrlnd and dcrlncli with all submodules.")
	$(GOBUILD) -tags=$(ALL_TAGS) -o dcrlnd-debug $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOBUILD) -tags=$(ALL_TAGS) -o dcrlncli-debug $(LDFLAGS) $(PKG)/cmd/dcrlncli

install:
	@$(call print, "Installing dcrlnd and dcrlncli.")
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlnd
	$(GOINSTALL) -tags="${tags}" $(LDFLAGS) $(PKG)/cmd/dcrlncli

scratch: build


# =======
# TESTING
# =======

check: unit itest

itest-only:
	@$(call print, "Running integration tests with ${backend} backend.")
	$(ITEST)

itest: dcrd dcrwallet build-itest itest-only

unit-only:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit: dcrd dcrwallet unit-only

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG) -- -test.tags="$(DEV_TAGS) $(LOG_TAGS)"


unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE)

goveralls: $(GOVERALLS_BIN)
	@$(call print, "Sending coverage report.")
	$(GOVERALLS_BIN) -coverprofile=coverage.txt -service=travis-ci

ci-race: dcrd dcrwallet unit-race

travis-cover: dcrd dcrwallet unit-cover goveralls

ci-itest: itest

# =============
# FLAKE HUNTING
# =============

flakehunter: build-itest
	@$(call print, "Flake hunting ${backend} integration tests.")
	while [ $$? -eq 0 ]; do $(ITEST); done

flake-unit:
	@$(call print, "Flake hunting unit tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(UNIT) -count=1; done

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

mobile-rpc:
	@$(call print, "Creating mobile RPC from protos.")
	cd ./mobile; ./gen_bindings.sh

vendor:
	@$(call print, "Re-creating vendor directory.")
	rm -r vendor/; GO111MODULE=on go mod vendor

ios: vendor mobile-rpc
	@$(call print, "Building iOS framework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=ios -tags="ios $(DEV_TAGS) autopilotrpc experimental" $(LDFLAGS) -v -o $(IOS_BUILD) $(MOBILE_PKG)

android: vendor mobile-rpc
	@$(call print, "Building Android library ($(ANDROID_BUILD)).")
	mkdir -p $(ANDROID_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=android -tags="android $(DEV_TAGS) autopilotrpc experimental" $(LDFLAGS) -v -o $(ANDROID_BUILD) $(MOBILE_PKG)

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
	mobile-rpc \
	vendor \
	ios \
	android \
	mobile \
	clean
