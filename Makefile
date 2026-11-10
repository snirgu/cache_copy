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

# -s -w strips the symbol table and DWARF; the version is compiled in so the
# running binary reports it in --help without reading a file next to it.
LDFLAGS  := -s -w -X main.VERSION=$(VERSION)
GOBUILD  := go build -mod=vendor -trimpath -ldflags "$(LDFLAGS)"

.DEFAULT_GOAL := help
.PHONY: help build build-windows build-linux test lint fmt check \
        version bump vendor release push clean

## ---------------------------------------------------------------- help ----

help:
	@echo cache_copy $(VERSION)
	@echo   Build
	@echo     make build           Build Windows and Linux binaries into $(BIN_DIR)/
	@echo     make build-windows   Build $(BIN_WIN)
	@echo     make build-linux     Build $(BIN_LIN)
	@echo   Quality
	@echo     make test            Go tests
	@echo     make lint            go vet
	@echo     make fmt             Format Go sources
	@echo     make check           lint + test
	@echo   Version
	@echo     make version         Print the current version
	@echo     make bump V=1.0.4    Write a new version to the VERSION file
	@echo   Release
	@echo     make vendor          Refresh vendored Go modules
	@echo     make release V=1.0.4 Bump VERSION, build both binaries, commit, tag v1.0.4
	@echo     make push            Push the current branch and its tags to origin
	@echo     make clean           Remove untracked build artifacts in $(BIN_DIR)/

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
# binaries were built from. Every guard below is evaluated before the VERSION
# file is written, so a failed guard leaves nothing changed.
release:
	$(if $(V),,$(error Usage: make release V=x.y.z))
	$(if $(strip $(shell git status --porcelain)),$(error Working tree is not clean - commit or stash first))
	$(if $(strip $(shell git tag -l v$(V))),$(error Tag v$(V) already exists))
	$(file > VERSION,$(V))
	$(MAKE) check build V=$(V)
	git add VERSION
	git add -u $(BIN_DIR)
	git commit -m "Release v$(V)"
	git tag -a v$(V) -m "Release v$(V)"
	@echo Tagged v$(V). Run make push to publish.

push:
	git push origin HEAD
	git push origin --tags

# Removes only git-ignored files under bin/ (shell-agnostic, unlike rm/del).
# The tracked Linux binary is left alone.
clean:
	git clean -fX $(BIN_DIR)
