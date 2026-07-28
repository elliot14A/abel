package cli

import (
	"context"
	"fmt"

	"github.com/elliot14A/abel/internal/mcpserver"
)

type mcpCmd struct {
	Pull bool `help:"Pull images even if they are already present locally."`
}

func (c *mcpCmd) Run(ctx context.Context, deps *deps) error {
	// The Docker client is built up front: an agent discovering mid-session
	// that the daemon is down is worse than abel refusing to start.
	//
	// Pull progress goes to stderr, never stdout — stdout is the JSON-RPC
	// stream, and one stray byte on it ends the session.
	runJob, err := deps.runJob(ctx, c.Pull, deps.stdio.Err)
	if err != nil {
		return err
	}

	server := mcpserver.New(Version, mcpserver.UseCases{
		RunJob:     runJob,
		GetFailure: deps.getFailure(),
		MarkFixed:  deps.markFixed(),
		ListJobs:   deps.listJobs(),
	})

	fmt.Fprintf(deps.stdio.Err, "abel %s: MCP server ready on stdio (repo: %s)\n", Version, deps.repoRoot)
	return mcpserver.Serve(ctx, server, deps.stdio.In, deps.stdio.Out)
}
