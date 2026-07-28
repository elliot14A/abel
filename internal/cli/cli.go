// Package cli is abel's command-line transport and composition root.
//
// It owns argument parsing, the user's terminal, and the wiring that injects
// concrete adapters into the use-cases. It contains no business logic: every
// command here is a thin shell around something in internal/app, which is what
// keeps it interchangeable with the MCP transport.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/alecthomas/kong"

	"github.com/elliot14A/abel/internal/infra/store"
	"github.com/elliot14A/abel/internal/infra/workflowfile"
)

// Version is stamped at build time by goreleaser:
//
//	-ldflags "-X github.com/elliot14A/abel/internal/cli.Version=$VERSION"
var Version = "dev"

const description = `abel — run your CI locally before you push.

Reproduce a GitHub Actions job's run: steps in the real container, stream the
logs, and when a step fails, capture the failure context so your coding agent
can fix it — then re-run.`

// IO is the process's standard streams, passed explicitly so that tests drive
// the CLI in-process without touching os.Stdout.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

// Globals are the flags every command accepts.
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

// Main parses args and runs the requested command, returning the process exit
// code. It never calls os.Exit, so main stays trivial and tests can call it
// directly.
func Main(ctx context.Context, args []string, stdio IO) int {
	var g grammar
	parser, err := kong.New(&g,
		kong.Name("abel"),
		kong.Description(description),
		kong.UsageOnError(),
		kong.Writers(stdio.Out, stdio.Err),
		// Without this kong calls os.Exit from inside Parse, which would make
		// the CLI untestable in-process and skip our cleanup.
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
		// kong has already printed usage; classify it as a usage error so the
		// exit code matches the rest of the taxonomy.
		fmt.Fprintf(stdio.Err, "abel: %v\n", err)
		return ExitUsage
	}

	deps, err := newDeps(g.Globals, stdio)
	if err != nil {
		fmt.Fprint(stdio.Err, deps.errorText(err))
		return ExitCode(err)
	}
	defer deps.close()

	// context.Context is an interface, so kong needs BindTo rather than the
	// concrete-type binding Run(...) would do.
	kctx.BindTo(ctx, (*context.Context)(nil))
	if err := kctx.Run(deps); err != nil {
		fmt.Fprint(stdio.Err, deps.errorText(err))
		return ExitCode(err)
	}
	return deps.exitCode
}

type versionCmd struct{}

func (c *versionCmd) Run(deps *deps) error {
	fmt.Fprintf(deps.stdio.Out, "abel %s\n", Version)
	return nil
}

type jobsCmd struct{}

func (c *jobsCmd) Run(ctx context.Context, deps *deps) error {
	refs, err := deps.listJobs().Execute(ctx)
	if err != nil {
		return err
	}
	fmt.Fprint(deps.stdio.Out, deps.ui.Jobs(refs))
	return nil
}

type failureCmd struct {
	Job  string `arg:"" help:"Job whose captured failure to show."`
	JSON bool   `help:"Print the raw failure context as JSON — the same payload the MCP tool returns."`
}

func (c *failureCmd) Run(ctx context.Context, deps *deps) error {
	failure, err := deps.getFailure().Execute(ctx, c.Job)
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(deps.stdio.Out, failure)
	}
	fmt.Fprint(deps.stdio.Out, deps.ui.Failure(failure))
	// Showing a failure is a successful command, but the failure is still a
	// failure: exit non-zero so `abel failure lint && deploy` does the right
	// thing.
	deps.exitCode = ExitStepFailed
	return nil
}
