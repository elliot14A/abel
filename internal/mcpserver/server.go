package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

type Tools struct {
	RunJob     *app.RunJob
	GetFailure *app.GetFailure
	MarkFixed  *app.MarkFixed
	ListJobs   *app.ListJobs
	Log        *slog.Logger
}

func New(version string, t Tools) *mcp.Server {
	log := t.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "abel",
		Title:   "abel - local CI reproduction",
		Version: version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_jobs",
		Description: "List the GitHub Actions jobs abel can reproduce locally, " +
			"with the workflow file each comes from. Call this first if you do not " +
			"know the job name.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, wrap(log, listJobs(t.ListJobs)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "run_job",
		Description: "Reproduce a workflow job locally in Docker and report the result. " +
			"On failure the failure context is captured and can be read with get_failure. " +
			"This starts a container and runs the job's shell steps against the working tree, " +
			"so it modifies files exactly as CI would. Set `output` to \"all\" to see what " +
			"steps that passed printed. Set `timeout` (seconds) for any job that might not " +
			"terminate, such as one that starts a server or a file watcher; without it a " +
			"hanging step blocks this call indefinitely.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: ptr(false),
		},
	}, wrap(log, runJob(t.RunJob)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "plan_job",
		Description: "Resolve a job and report what abel would run: the image, the steps " +
			"in order, which are skipped and why, and any warnings. Starts no container " +
			"and does not touch the working tree. Call this before run_job when you need " +
			"to know what a job will do.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, wrap(log, planJob(t.RunJob)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_failure",
		Description: "Return the failure context captured by the last run of a job: " +
			"the failing step, its command, exit code, the tail of its output, and its " +
			"environment. Secrets are redacted. If the tail is too short to diagnose the " +
			"failure, re-run with run_job's `tail` set higher.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, wrap(log, getFailure(t.GetFailure)))

	mcp.AddTool(server, &mcp.Tool{
		Name: "mark_fixed",
		Description: "Record that you believe a job's failure is fixed, with a short note " +
			"saying what you changed. The note is shown to the developer reviewing the diff. " +
			"This verifies nothing; call run_job afterwards to confirm.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: false},
	}, wrap(log, markFixed(t.MarkFixed)))

	return server
}

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

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func IsCleanShutdown(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	text := err.Error()
	return strings.Contains(text, "server is closing") ||
		strings.Contains(text, "connection closed")
}

type NoInput struct{}

type JobInput struct {
	Job string `json:"job" jsonschema:"the workflow job ID to act on"`
}

type RunJobInput struct {
	JobInput
	Image   string `json:"image,omitempty" jsonschema:"optional container image override"`
	Tail    int    `json:"tail,omitempty" jsonschema:"log lines to keep in the failure context; default 200"`
	Output  string `json:"output,omitempty" jsonschema:"which steps return their output: failed (default) or all"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"seconds before the run is abandoned; unset means no limit"`
}

const (
	outputAll   = "all"
	opRunJobMCP = "mcpserver.run_job"
)

type RunJobOutput struct {
	Job      string       `json:"job"`
	Passed   bool         `json:"passed"`
	Image    string       `json:"image"`
	Summary  string       `json:"summary"`
	Steps    []StepOutput `json:"steps"`
	Warnings []string     `json:"warnings,omitempty"`
	Failure  *run.Failure `json:"failure,omitempty"`
}

type StepOutput struct {
	Index      int      `json:"index"`
	Name       string   `json:"name"`
	ExitCode   int      `json:"exit_code"`
	Skipped    bool     `json:"skipped"`
	SkipReason string   `json:"skip_reason,omitempty"`
	Output     []string `json:"output,omitempty"`
}

type PlanJobOutput struct {
	Job      string       `json:"job"`
	Image    string       `json:"image"`
	Source   string       `json:"workflow_path"`
	Steps    []StepOutput `json:"steps"`
	Warnings []string     `json:"warnings,omitempty"`
}

type JobsOutput struct {
	Jobs []app.JobRef `json:"jobs"`
}

type FailureOutput struct {
	Failure run.Failure `json:"failure"`
}

type MarkFixedInput struct {
	JobInput
	Note string `json:"note,omitempty" jsonschema:"what you changed, shown to the developer reviewing the diff"`
}

type MarkFixedOutput struct {
	Job      string `json:"job"`
	Fixed    bool   `json:"fixed"`
	NextStep string `json:"next_step"`
}

func listJobs(uc *app.ListJobs) func(context.Context, *mcp.CallToolRequest, NoInput) (JobsOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (JobsOutput, error) {
		refs, err := uc.Execute(ctx)
		return JobsOutput{Jobs: refs}, err
	}
}

func runJob(uc *app.RunJob) func(context.Context, *mcp.CallToolRequest, RunJobInput) (RunJobOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in RunJobInput) (RunJobOutput, error) {
		plan, err := uc.Plan(ctx, in.Job, workflow.Options{Image: in.Image})
		if err != nil {
			return RunJobOutput{}, err
		}

		runCtx := ctx
		if in.Timeout > 0 {
			timed, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
			defer cancel()
			runCtx = timed
		}

		runIn := app.RunJobInput{
			JobID:         in.Job,
			Resolve:       workflow.Options{Image: in.Image},
			LogTailLines:  in.Tail,
			CaptureOutput: in.Output == outputAll,
		}
		if report := progressReporter(ctx, req, len(plan.Steps)); report != nil {
			runIn.OnStepEnd = report
		}

		result, err := uc.Execute(runCtx, runIn)
		if err != nil {
			return RunJobOutput{}, timeoutError(runCtx, ctx, err, in)
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
				Name:       run.RedactText(s.Step.Name, s.Step.Env),
				ExitCode:   s.ExitCode,
				Skipped:    s.Skipped,
				SkipReason: s.Step.SkipReason,
				Output:     s.Output,
			})
		}
		for _, w := range plan.Warnings {
			out.Warnings = append(out.Warnings, w.String())
		}
		return out, nil
	}
}

func progressReporter(ctx context.Context, req *mcp.CallToolRequest, total int) func(run.StepResult) {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}

	return func(result run.StepResult) {
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(result.Step.Index + 1),
			Total:         float64(total),
			Message:       run.RedactText(result.Step.Name, result.Step.Env),
		})
	}
}

func timeoutError(runCtx, parent context.Context, err error, in RunJobInput) error {
	if in.Timeout <= 0 || parent.Err() != nil ||
		!errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return err
	}
	return errs.New(errs.KindCancelled, opRunJobMCP,
		"job %q did not finish within its %ds timeout; raise run_job's `timeout`, "+
			"or the step it stopped on does not terminate", in.Job, in.Timeout).
		With("job", in.Job).Wrapping(err)
}

func planJob(uc *app.RunJob) func(context.Context, *mcp.CallToolRequest, RunJobInput) (PlanJobOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RunJobInput) (PlanJobOutput, error) {
		plan, err := uc.Plan(ctx, in.Job, workflow.Options{Image: in.Image})
		if err != nil {
			return PlanJobOutput{}, err
		}

		out := PlanJobOutput{
			Job:      plan.JobID,
			Image:    plan.Image,
			Source:   plan.Source,
			Steps:    make([]StepOutput, 0, len(plan.Steps)),
			Warnings: make([]string, 0, len(plan.Warnings)),
		}
		for _, s := range plan.Steps {
			out.Steps = append(out.Steps, StepOutput{
				Index:      s.Index,
				Name:       run.RedactText(s.Name, s.Env),
				Skipped:    s.Skip,
				SkipReason: s.SkipReason,
			})
		}
		for _, w := range plan.Warnings {
			out.Warnings = append(out.Warnings, w.String())
		}
		return out, nil
	}
}

func getFailure(uc *app.GetFailure) func(context.Context, *mcp.CallToolRequest, JobInput) (FailureOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in JobInput) (FailureOutput, error) {
		failure, err := uc.Execute(ctx, in.Job)
		return FailureOutput{Failure: failure}, err
	}
}

func markFixed(uc *app.MarkFixed) func(context.Context, *mcp.CallToolRequest, MarkFixedInput) (MarkFixedOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in MarkFixedInput) (MarkFixedOutput, error) {
		failure, err := uc.Execute(ctx, in.Job, in.Note)
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

func wrap[In, Out any](
	log *slog.Logger, h func(context.Context, *mcp.CallToolRequest, In) (Out, error),
) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		name := ""
		if req != nil && req.Params != nil {
			name = req.Params.Name
		}
		log.Info("tool_call", "tool", name)

		out, err := h(ctx, req, in)
		if err != nil {
			var zero Out

			log.Error("tool_failed", "tool", name, "kind", string(errs.KindOf(err)), "error", err.Error())
			return nil, zero, &agentError{err: err}
		}
		log.Debug("tool_ok", "tool", name)
		return nil, out, nil
	}
}

type agentError struct{ err error }

func (e *agentError) Error() string { return agentMessage(e.err) }
func (e *agentError) Unwrap() error { return e.err }

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
			"in the workflow; do not try to work around it by editing the workflow file.")
	case errs.KindCancelled:
		b.WriteString("\n\nHint: the developer interrupted the run.")
	case errs.KindValidation, errs.KindConflict, errs.KindStepFailed, errs.KindInternal:
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }
