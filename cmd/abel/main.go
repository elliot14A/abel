// Command abel reproduces a GitHub Actions job locally in Docker and serves
// the failure context to a coding agent.
//
// This file is the entrypoint and nothing else: it owns the process — signals,
// standard streams, the exit code — and hands everything else to
// internal/cli.Main, which is testable in-process.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/elliot14A/abel/internal/cli"
)

func main() {
	// The first interrupt cancels the context so containers are cleaned up and
	// the failure context is still written; stop() restores the default
	// handler, so a second Ctrl-C kills abel outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Main(ctx, os.Args[1:], cli.IO{
		In:  os.Stdin,
		Out: os.Stdout,
		Err: os.Stderr,
	}))
}
