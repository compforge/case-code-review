.PHONY: build install test clean run help fix fmt vet check \
	build-all dist sha256sum version-info \
	build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 \
	build-windows-amd64 build-windows-arm64

BINARY_NAME := ccr
GO          := go
DIST_DIR    := ./dist
INSTALL_DIR ?= $(HOME)/.local/bin
TEST_FILES  ?=

# Go tests compile at package scope, so focused test files select their packages.
TEST_PACKAGES ?= $(if $(strip $(TEST_FILES)),$(sort $(foreach file,$(TEST_FILES),./$(patsubst %/,%,$(dir $(patsubst ./%,%,$(file)))))),./...)

# Version info — VERSION is the release source of truth; commits identify the
# exact build within that release line.
GIT_COMMIT  := $(shell git rev-parse --short HEAD)
BUILD_DATE  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

VERSION     ?= $(shell cat VERSION 2>/dev/null || echo v0.0.0)

LD_FLAGS    := -s -w \
	-X main.Version=$(VERSION) \
	-X main.GitCommit=$(GIT_COMMIT) \
	-X main.BuildDate=$(BUILD_DATE)

define BUILD_PLATFORM
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 $(GO) build -ldflags "$(LD_FLAGS)" \
		-o $(DIST_DIR)/$(BINARY_NAME)-$(1)-$(2)$(3) \
		./cmd/ccr
endef

# ── Development targets ──────────────────────────────────────────────────────
build:
	$(GO) build -ldflags "$(LD_FLAGS)" -o $(DIST_DIR)/$(BINARY_NAME) ./cmd/ccr

# Install the freshly built binary to INSTALL_DIR (default ~/.local/bin).
# On macOS, ad-hoc re-sign after the copy: cp invalidates the code signature and
# the OS then SIGKILLs the binary on first run; `codesign --sign -` restores it.
install: build
	mkdir -p $(INSTALL_DIR)
	cp $(DIST_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@if [ "$$(uname)" = "Darwin" ]; then codesign --force --sign - $(INSTALL_DIR)/$(BINARY_NAME) && echo "re-signed for macOS"; fi
	@echo "installed $(BINARY_NAME) $(VERSION) -> $(INSTALL_DIR)/$(BINARY_NAME)"

test:
	LC_ALL=C $(GO) test -v -race -count=1 $(TEST_PACKAGES)

clean:
	rm -rf $(DIST_DIR)

run: build
	$(DIST_DIR)/$(BINARY_NAME) --staged

help: build
	$(DIST_DIR)/$(BINARY_NAME) -h

fix:
	$(GO) fmt ./...

fmt: fix

vet:
	$(GO) vet ./...

check:
	$(GO) mod tidy
	$(GO) fmt ./...
	$(GO) vet ./...
	@echo "check passed"

# ── Cross-platform targets ───────────────────────────────────────────────────
build-linux-amd64:
	$(call BUILD_PLATFORM,linux,amd64)

build-linux-arm64:
	$(call BUILD_PLATFORM,linux,arm64)

build-darwin-amd64:
	$(call BUILD_PLATFORM,darwin,amd64)

build-darwin-arm64:
	$(call BUILD_PLATFORM,darwin,arm64)

build-windows-amd64:
	$(call BUILD_PLATFORM,windows,amd64,.exe)

build-windows-arm64:
	$(call BUILD_PLATFORM,windows,arm64,.exe)

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64 build-windows-arm64

# Generate SHA256 checksums for all release binaries
sha256sum: build-all
	cd $(DIST_DIR) && shasum -a 256 $(BINARY_NAME)-* | sort > sha256sum.txt

# Full release: clean → build all platforms → checksums
dist: clean build-all sha256sum
	@echo $(VERSION) > $(DIST_DIR)/VERSION

version-info:
	@echo "Version:   $(VERSION)"
	@echo "GitCommit: $(GIT_COMMIT)"
	@echo "BuildDate: $(BUILD_DATE)"
	@echo "LD_FLAGS:  $(LD_FLAGS)"
