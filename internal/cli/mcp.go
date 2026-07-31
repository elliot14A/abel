package cli

import (
	"context"
	"fmt"

	"github.com/elliot14A/abel/internal/mcpserver"
)

type mcpCmd struct {
	Pull bool `help:"Pull images even if they are already present locally."`
}

func (c *mcpCmd) Run(ctx context.Context, a *abel) error {
	runJob, err := a.runJob(ctx, c.Pull, a.pullProgress())
	if err != nil {
		return err
	}

	server := mcpserver.New(Version, mcpserver.Tools{
		RunJob:     runJob,
		GetFailure: a.getFailure(),
		MarkFixed:  a.markFixed(),
		ListJobs:   a.listJobs(),
		Log:        a.log,
	})

	fmt.Fprintf(a.stdio.Err, "abel %s: MCP server ready on stdio (repo: %s)\n", Version, a.repoRoot)
	return mcpserver.Serve(ctx, server, a.stdio.In, a.stdio.Out)
}
