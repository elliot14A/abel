// Package mcpserver exposes abel's use-cases to a coding agent over MCP.
//
// This is abel's second transport, and it is the reason the rings exist: the
// tools below are thin adapters over exactly the same use-cases the CLI drives.
// Nothing here decides anything.
//
// The tool surface is the shared `agentfix` contract abel implements alongside
// mob — run_* / get_* / mark_fixed — so an agent that can drive one can drive
// the other.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

// UseCases are the operations the server exposes. They are passed in rather
// than constructed here, because the composition root owns construction.
type UseCases struct {
	RunJob     *app.RunJob
	GetFailure *app.GetFailure
	MarkFixed  *app.MarkFixed
	ListJobs   *app.ListJobs
}

// New builds the MCP server with abel's tools registered.
func New(version string, uc UseCases) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "abel",
		Title:   "abel — local CI reproduction",
		Version: version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_jobs",
		Description: "List the GitHub Actions jobs abel can reproduce locally, " +
			"with the workflow file each comes from. Call this first if you do not " +
			"know the job name.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, wrap(listJobs(uc.ListJobs)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "run_job",
		Description: "Reproduce a workflow job locally in Docker and report the result. " +
			"On failure the failure context is captured and can be read with get_failure. " +
			"This starts a container and runs the job's shell steps against the working tree, " +
			"so it modifies files exactly as CI would.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: ptr(false),
		},
	}, wrap(runJob(uc.RunJob)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_failure",
		Description: "Return the failure context captured by the last run of a job: " +
			"the failing step, its command, exit code, the tail of its output, and its " +
			"environment. Secrets are redacted.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, wrap(getFailure(uc.GetFailure)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "mark_fixed",
		Description: "Record that you believe a job's failure is fixed. This does not " +
			"verify anything — call run_job afterwards to confirm, and let the developer " +
			"review the diff.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: false},
	}, wrap(markFixed(uc.MarkFixed)))

	return server
}

// Serve runs the server over the given streams until the client disconnects or
// ctx ends.
//
// The streams are parameters rather than os.Stdin/os.Stdout — the SDK's
// StdioTransport reads the process's own — so the composition root stays the
// only thing that knows about the process, and a test can drive a whole
// session over pipes.
//
// Nothing else may write to out while this runs: it is the JSON-RPC stream.
// abel's logger and its readiness banner both go to stderr for this reason.
func Serve(ctx context.Context, server *mcp.Server, in io.Reader, out io.Writer) error {
	transport := &mcp.IOTransport{
		Reader: io.NopCloser(in),
		Writer: nopCloser{out},
	}
	err := server.Run(ctx, transport)
	if err == nil || IsCleanShutdown(err) {
		return nil
	}
	return errs.New(errs.KindOf(err), "mcpserver.Serve", "the MCP session ended").Wrapping(err)
}

// nopCloser adapts a Writer to the WriteCloser the transport wants. Closing
// abel's stdout is the composition root's business, not the transport's.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// IsCleanShutdown reports whether err is the ordinary end of a session rather
// than a failure.
//
// An agent closing its end of the pipe, or the developer pressing Ctrl-C, is
// how every MCP session ends. Reporting either as an error would make `abel
// mcp` exit non-zero on every normal run — and a supervisor that restarts on
// non-zero would loop forever.
func IsCleanShutdown(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// The stdio transport reports a closed pipe as "server is closing: EOF",
	// formatted with %v rather than %w, and its sentinel lives in the SDK's
	// internal/jsonrpc2 package — so there is nothing to match on structurally.
	// This is the one place in abel that inspects an error's text, and it is
	// only ever applied to a third-party error.
	//
	// If an SDK upgrade changes this wording, `abel mcp` starts exiting 70 on
	// every normal session; TestMCPServesOverPipesAndExitsCleanly (build tag:
	// integration) is what catches that.
	text := err.Error()
	return strings.Contains(text, "server is closing") ||
		strings.Contains(text, "connection closed")
}

// --- tool inputs and outputs ------------------------------------------------
//
// These structs are the tool schemas: the SDK infers JSON Schema from them, so
// the field comments below are what the agent actually reads.

// NoInput is the input schema of a tool that takes no arguments. It exists so
// list_jobs does not inherit JobInput's required "job" property.
type NoInput struct{}

// JobInput identifies a job.
type JobInput struct {
	// Job is the workflow job ID, as it appears under `jobs:` in the workflow
	// file. Use list_jobs if you do not know it.
	Job string `json:"job" jsonschema:"the workflow job ID to act on"`
}

// RunJobInput asks abel to reproduce a job.
type RunJobInput struct {
	JobInput
	// Image overrides the container image for every step.
	Image string `json:"image,omitempty" jsonschema:"optional container image override"`
}

// RunJobOutput is the result of a run.
type RunJobOutput struct {
	Job      string       `json:"job"`
	Passed   bool         `json:"passed"`
	Image    string       `json:"image"`
	Summary  string       `json:"summary"`
	Steps    []StepOutput `json:"steps"`
	Warnings []string     `json:"warnings,omitempty"`
	// Failure is present exactly when Passed is false.
	Failure *run.Failure `json:"failure,omitempty"`
}

// StepOutput is one step's outcome.
type StepOutput struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	ExitCode   int    `json:"exit_code"`
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// JobsOutput lists the jobs abel can reproduce.
type JobsOutput struct {
	Jobs []app.JobRef `json:"jobs"`
}

// FailureOutput carries a captured failure.
type FailureOutput struct {
	Failure run.Failure `json:"failure"`
}

// MarkFixedOutput confirms a job was marked fixed.
type MarkFixedOutput struct {
	Job      string `json:"job"`
	Fixed    bool   `json:"fixed"`
	NextStep string `json:"next_step"`
}

// --- handlers ---------------------------------------------------------------

func listJobs(uc *app.ListJobs) func(context.Context, NoInput) (JobsOutput, error) {
	return func(ctx context.Context, _ NoInput) (JobsOutput, error) {
		refs, err := uc.Execute(ctx)
		return JobsOutput{Jobs: refs}, err
	}
}

func runJob(uc *app.RunJob) func(context.Context, RunJobInput) (RunJobOutput, error) {
	return func(ctx context.Context, in RunJobInput) (RunJobOutput, error) {
		plan, err := uc.Plan(ctx, in.Job, workflow.Options{Image: in.Image})
		if err != nil {
			return RunJobOutput{}, err
		}

		// Logs are nil: an agent wants the captured tail, not a live stream it
		// would have to buffer anyway.
		result, err := uc.Execute(ctx, app.RunJobInput{
			JobID:   in.Job,
			Resolve: workflow.Options{Image: in.Image},
		})
		if err != nil {
			return RunJobOutput{}, err
		}

		out := RunJobOutput{
			Job:      result.JobID,
			Passed:   result.OK(),
			Image:    result.Image,
			Summary:  result.Summary(),
			Failure:  result.Failure,
			Steps:    make([]StepOutput, 0, len(result.Steps)),
			Warnings: make([]string, 0, len(plan.Warnings)),
		}
		for _, s := range result.Steps {
			out.Steps = append(out.Steps, StepOutput{
				Index:      s.Step.Index,
				Name:       s.Step.Name,
				ExitCode:   s.ExitCode,
				Skipped:    s.Skipped,
				SkipReason: s.Step.SkipReason,
			})
		}
		for _, w := range plan.Warnings {
			out.Warnings = append(out.Warnings, w.String())
		}
		return out, nil
	}
}

func getFailure(uc *app.GetFailure) func(context.Context, JobInput) (FailureOutput, error) {
	return func(ctx context.Context, in JobInput) (FailureOutput, error) {
		failure, err := uc.Execute(ctx, in.Job)
		return FailureOutput{Failure: failure}, err
	}
}

func markFixed(uc *app.MarkFixed) func(context.Context, JobInput) (MarkFixedOutput, error) {
	return func(ctx context.Context, in JobInput) (MarkFixedOutput, error) {
		failure, err := uc.Execute(ctx, in.Job)
		if err != nil {
			return MarkFixedOutput{}, err
		}
		return MarkFixedOutput{
			Job:      failure.JobID,
			Fixed:    failure.Fixed,
			NextStep: fmt.Sprintf("call run_job with job=%q to verify the fix", failure.JobID),
		}, nil
	}
}

// --- error mapping ----------------------------------------------------------

// wrap adapts a plain use-case function to an MCP tool handler and is the
// single place abel's error taxonomy meets MCP — the counterpart of
// cli.ExitCode.
//
// Errors become tool errors rather than protocol errors: the agent is supposed
// to read them and react, not treat the session as broken.
func wrap[In, Out any](h func(context.Context, In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := h(ctx, in)
		if err != nil {
			var zero Out
			// Returning the error rather than a hand-built result is deliberate:
			// the SDK turns it into an IsError result *and* skips validating the
			// zero output against the tool's output schema, which a hand-built
			// error result does not.
			return nil, zero, &agentError{err: err}
		}
		return nil, out, nil
	}
}

// agentError renders an abel error the way an agent should read it. It keeps
// the wrapped error in the chain so errors.Is/As still work above it.
type agentError struct{ err error }

func (e *agentError) Error() string { return agentMessage(e.err) }
func (e *agentError) Unwrap() error { return e.err }

// agentMessage turns an error into something an agent can act on. The Kind is
// included because it tells the agent whether to retry, ask the user, or give
// up — which a prose message alone does not.
func agentMessage(err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", errs.KindOf(err), err.Error())

	switch errs.KindOf(err) {
	case errs.KindNotFound:
		b.WriteString("\n\nHint: call list_jobs to see the job names abel can reproduce.")
	case errs.KindDependency:
		b.WriteString("\n\nHint: abel needs a running Docker daemon. Ask the developer to start it; " +
			"do not retry in a loop.")
	case errs.KindUnsupported:
		b.WriteString("\n\nHint: abel reproduces `run:` steps only. This is a limitation, not a bug " +
			"in the workflow — do not try to work around it by editing the workflow file.")
	case errs.KindCancelled:
		b.WriteString("\n\nHint: the developer interrupted the run.")
	case errs.KindValidation, errs.KindConflict, errs.KindStepFailed, errs.KindInternal:
		// No extra guidance: the message already says what to do.
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }
