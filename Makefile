BINARY  := abel
PKG     := github.com/elliot14A/abel
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help:
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'

.PHONY: build
build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

.PHONY: install
install:
	go install -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

.PHONY: test
test:
	go test -race -shuffle=on ./...

.PHONY: test-integration
test-integration:
	go test -race -tags integration -count=1 ./...

.PHONY: fuzz
fuzz:
	go test ./internal/core/workflow -run=Fuzz -fuzz=FuzzParse -fuzztime=60s

.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html && echo "wrote coverage.html"

.PHONY: fmt
fmt:
	go tool golangci-lint fmt

.PHONY: lint
lint:
	go vet ./...
	go tool golangci-lint fmt --diff
	go tool golangci-lint run

.PHONY: vuln
vuln:
	go tool govulncheck ./...

.PHONY: check
check: lint test vuln

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

.PHONY: snapshot
snapshot:
	go tool goreleaser release --snapshot --clean

.PHONY: clean
clean:
	rm -rf bin dist coverage.out coverage.html
