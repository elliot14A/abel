## abel — run your CI locally before you push.
##
## Every target here is what CI runs, so a green `make check` means a green CI.
## Dev tools are pinned as `tool` directives in go.mod: `go tool <name>` uses
## the exact version this repo declares, never whatever is on your PATH.

BINARY  := abel
PKG     := github.com/elliot14A/abel
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

.DEFAULT_GOAL := help

## help: list the available targets
.PHONY: help
help:
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'

## build: build the binary into ./bin
.PHONY: build
build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

## install: install abel into GOBIN
.PHONY: install
install:
	go install -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

## test: unit tests with the race detector and shuffled order
.PHONY: test
test:
	go test -race -shuffle=on ./...

## test-integration: tests that need a real Docker daemon
.PHONY: test-integration
test-integration:
	go test -race -tags integration -count=1 ./...

## fuzz: run the workflow-parser fuzzer for 60s
.PHONY: fuzz
fuzz:
	go test ./internal/core/workflow -run=Fuzz -fuzz=FuzzParse -fuzztime=60s

## cover: unit-test coverage report (target: ~85% on core and app)
.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html && echo "wrote coverage.html"

## fmt: format the tree (gofmt + gofumpt + import grouping)
.PHONY: fmt
fmt:
	go tool golangci-lint fmt

## lint: vet + golangci-lint, including the depguard ring rules
.PHONY: lint
lint:
	go vet ./...
	go tool golangci-lint fmt --diff
	go tool golangci-lint run

## vuln: report reachable known vulnerabilities
.PHONY: vuln
vuln:
	go tool govulncheck ./...

## check: everything CI runs
.PHONY: check
check: lint test vuln

## tidy: tidy and verify go.mod / go.sum
.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

## snapshot: build release artifacts locally without publishing
.PHONY: snapshot
snapshot:
	go tool goreleaser release --snapshot --clean

## clean: remove build output
.PHONY: clean
clean:
	rm -rf bin dist coverage.out coverage.html
