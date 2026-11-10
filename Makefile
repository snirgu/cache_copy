# cache_copy — single entry point for build and release commands.
#
# Requires GNU Make >= 4.2 (for $(file <) / $(file >), which avoid needing cat/echo >).
#   winget install ezwinports.make
#
# Recipes are deliberately shell-agnostic: no sed/grep/rm, no VAR=x cmd
# prefixes. Make on Windows falls back to cmd.exe when sh.exe is not on PATH,
# so anything unix-only would break. Cross-compile targets set GOOS/GOARCH via
# target-specific exported variables, which work under both shells.

# Single source of truth is the VERSION file (semver: MAJOR.MINOR.PATCH).
# Passing V=x.y.z on the command line overrides it for this run.
VERSION  := $(strip $(or $(V),$(file < VERSION)))
APP      := cache_copy
BIN_DIR  := bin
BIN_WIN  := $(BIN_DIR)/$(APP).exe
BIN_LIN  := $(BIN_DIR)/$(APP)

# Release files, named with version and platform, as attached to the GitHub
# Release. Binaries are not committed to git.
DIST     := dist
DIST_WIN := $(DIST)/$(APP)-$(VERSION)-windows-amd64.exe
DIST_LIN := $(DIST)/$(APP)-$(VERSION)-linux-amd64

# -s -w strips the symbol table and DWARF; the version is compiled in so the
# running binary reports it in --help without reading a file next to it.
LDFLAGS  := -s -w -X main.VERSION=$(VERSION)
GOBUILD  := go build -mod=vendor -trimpath -ldflags "$(LDFLAGS)"

.DEFAULT_GOAL := help
.PHONY: help build build-windows build-linux dist dist-windows dist-linux \
        test lint fmt check version bump vendor release push publish clean

## ---------------------------------------------------------------- help ----

help:
	@echo cache_copy $(VERSION)
	@echo   Build
	@echo     make build           Build Windows and Linux binaries into $(BIN_DIR)/
	@echo     make build-windows   Build $(BIN_WIN)
	@echo     make build-linux     Build $(BIN_LIN)
	@echo     make dist            Build the versioned release files into $(DIST)/
	@echo   Quality
	@echo     make test            Go tests
	@echo     make lint            go vet
	@echo     make fmt             Format Go sources
	@echo     make check           lint + test
	@echo   Version
	@echo     make version         Print the current version
	@echo     make bump V=1.0.6    Write a new version to the VERSION file
	@echo   Release
	@echo     make vendor          Refresh vendored Go modules
	@echo     make release V=1.0.6 Check, build, commit VERSION and tag v1.0.6
	@echo     make push            Push the current branch and the release tag
	@echo     make publish         Push, then create the GitHub Release with the binaries
	@echo     make clean           Remove build output in $(BIN_DIR)/ and $(DIST)/

## --------------------------------------------------------------- build ----

build: build-windows build-linux

build-windows: export GOOS := windows
build-windows: export GOARCH := amd64
build-windows:
	$(GOBUILD) -o $(BIN_WIN) .

build-linux: export GOOS := linux
build-linux: export GOARCH := amd64
build-linux:
	$(GOBUILD) -o $(BIN_LIN) .

dist: dist-windows dist-linux

dist-windows: export GOOS := windows
dist-windows: export GOARCH := amd64
dist-windows:
	$(GOBUILD) -o $(DIST_WIN) .

dist-linux: export GOOS := linux
dist-linux: export GOARCH := amd64
dist-linux:
	$(GOBUILD) -o $(DIST_LIN) .

## ------------------------------------------------------------- quality ----

test:
	go test -mod=vendor ./...

lint:
	go vet -mod=vendor ./...

fmt:
	go fmt ./...

check: lint test

## ------------------------------------------------------------- version ----

version:
	@echo $(VERSION)

bump:
	$(if $(V),,$(error Usage: make bump V=x.y.z))
	$(file > VERSION,$(V))
	@echo VERSION set to $(V)

## ------------------------------------------------------------- release ----

vendor:
	go mod tidy
	go mod vendor

# Requires a clean working tree so the tagged commit is exactly what the
# binaries are built from. Every guard below is evaluated before the VERSION
# file is written, so a failed guard leaves nothing changed. Only VERSION is
# committed; the binaries go to the GitHub Release (make publish).
release:
	$(if $(V),,$(error Usage: make release V=x.y.z))
	$(if $(strip $(shell git status --porcelain)),$(error Working tree is not clean - commit or stash first))
	$(if $(strip $(shell git tag -l v$(V))),$(error Tag v$(V) already exists))
	$(file > VERSION,$(V))
	$(MAKE) check dist V=$(V)
	git add VERSION
	git commit -m "Release v$(V)"
	git tag -a v$(V) -m "Release v$(V)"
	@echo Tagged v$(V). Run make publish to push it and create the GitHub Release.

push:
	git push origin HEAD
	git push origin v$(VERSION)

# Creates the GitHub Release for the current VERSION with both binaries
# attached. It refuses unless HEAD is exactly the release tag and the tree is
# clean, so the uploaded files are built from exactly the tagged code.
# Needs the GitHub CLI, logged in once:
#   winget install GitHub.cli
#   gh auth login
publish:
	$(if $(strip $(shell git status --porcelain)),$(error Working tree is not clean - commit or stash first))
	$(if $(filter $(shell git rev-parse HEAD),$(shell git rev-list -n 1 v$(VERSION))),,$(error HEAD is not at tag v$(VERSION) - check out the release commit first))
	$(MAKE) push dist
	gh release create v$(VERSION) $(DIST_WIN) $(DIST_LIN) --verify-tag --title "cache_copy v$(VERSION)" --generate-notes

# Removes only git-ignored files (shell-agnostic, unlike rm/del).
clean:
	git clean -fX $(BIN_DIR) $(DIST)
