package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/alecthomas/kong"

	"github.com/elliot14A/abel/internal/infra/store"
	"github.com/elliot14A/abel/internal/infra/workflowfile"
)

var Version = "dev"

const description = `abel - run your CI locally before you push.

Reproduce a GitHub Actions job's run: steps in the real container, stream the
logs, and when a step fails, capture the failure context so your coding agent
can fix it, then re-run.`

type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

type Globals struct {
	Repo      string `help:"Repository root to run against." default:"." type:"path" env:"ABEL_REPO"`
	Workflows string `help:"Workflow directory, relative to --repo." default:"${workflows}" env:"ABEL_WORKFLOWS"`
	State     string `help:"Directory for abel's captured failures, relative to --repo." default:"${state}" env:"ABEL_STATE"`
	Color     string `help:"When to colourise output." enum:"auto,always,never" default:"auto" env:"ABEL_COLOR"`
	LogLevel  string `help:"Structured log level on stderr (debug|info|warn|error)." default:"warn" env:"ABEL_LOG_LEVEL"`
}

type grammar struct {
	Globals
	Run     runCmd     `cmd:"" help:"Reproduce a workflow job locally in Docker."`
	Jobs    jobsCmd    `cmd:"" help:"List the jobs abel can run."`
	Failure failureCmd `cmd:"" help:"Show the failure context captured for a job."`
	MCP     mcpCmd     `cmd:"" name:"mcp" help:"Serve failures to a coding agent over MCP (stdio)."`
	Version versionCmd `cmd:"" help:"Print abel's version."`
}

func Main(ctx context.Context, args []string, stdio IO) int {
	var g grammar
	parser, err := kong.New(&g,
		kong.Name("abel"),
		kong.Description(description),
		kong.UsageOnError(),
		kong.Writers(stdio.Out, stdio.Err),

		kong.Exit(func(int) {}),
		kong.Vars{
			"workflows": workflowfile.DefaultDir,
			"state":     store.DefaultDir,
			"version":   Version,
		},
	)
	if err != nil {
		fmt.Fprintf(stdio.Err, "abel: cannot build the command grammar: %v\n", err)
		return ExitInternal
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		fmt.Fprintf(stdio.Err, "abel: %v\n", err)
		return ExitUsage
	}

	a, err := newAbel(g.Globals, stdio)
	if err != nil {
		fmt.Fprint(stdio.Err, a.errorText(err))
		return ExitCode(err)
	}
	defer a.close()

	kctx.BindTo(ctx, (*context.Context)(nil))
	if err := kctx.Run(a); err != nil {
		fmt.Fprint(stdio.Err, a.errorText(err))
		return a.exit(ExitCode(err), err)
	}
	return a.exit(a.exitCode, nil)
}

type versionCmd struct{}

func (c *versionCmd) Run(a *abel) error {
	fmt.Fprintf(a.stdio.Out, "abel %s\n", Version)
	return nil
}

type jobsCmd struct{}

func (c *jobsCmd) Run(ctx context.Context, a *abel) error {
	refs, err := a.listJobs().Execute(ctx)
	if err != nil {
		return err
	}
	fmt.Fprint(a.stdio.Out, a.ui.Jobs(refs))
	return nil
}

type failureCmd struct {
	Job  string `arg:"" help:"Job whose captured failure to show."`
	JSON bool   `help:"Print the raw failure context as JSON, the same payload the MCP tool returns."`
}

func (c *failureCmd) Run(ctx context.Context, a *abel) error {
	failure, err := a.getFailure().Execute(ctx, c.Job)
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(a.stdio.Out, failure)
	}
	fmt.Fprint(a.stdio.Out, a.ui.Failure(failure))

	a.exitCode = ExitStepFailed
	return nil
}
