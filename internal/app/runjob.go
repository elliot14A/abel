package app

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

const opRunJob = "app.RunJob"

// cleanupTimeout bounds container removal. It runs on a context detached from
// the caller's, so it needs its own deadline.
const cleanupTimeout = 30 * time.Second

// Terminal is the user's terminal, supplied only when a run may attach to the
// container (`--shell`). A zero Terminal means "never attach".
type Terminal struct {
	In       io.Reader
	Out, Err io.Writer
}

func (t Terminal) usable() bool { return t.In != nil && t.Out != nil }

// RunJobInput is one request to reproduce a job.
type RunJobInput struct {
	// JobID is the workflow job to run.
	JobID string
	// Logs receives the container's combined output as it is produced. A nil
	// Logs discards the live stream — the MCP server does that, since it reports
	// the captured tail rather than streaming to an agent.
	Logs io.Writer
	// Shell requests an interactive shell in the container after the steps have
	// finished or one has failed. Ignored unless Terminal is usable.
	Shell    bool
	Terminal Terminal
	// Resolve overrides image and workdir selection.
	Resolve workflow.Options
	// LogTailLines caps how much output a captured failure keeps. Zero uses
	// run.DefaultLogTailLines.
	LogTailLines int

	// OnStepStart and OnStepEnd observe the run as it happens. They exist so
	// the CLI can print a step's heading before its output and its verdict
	// after — without them the whole report arrives at the end, which reads as
	// a hang on a slow step. Both may be nil; the MCP transport leaves them so.
	OnStepStart func(run.Step)
	OnStepEnd   func(run.StepResult)
}

func (in RunJobInput) stepStarted(step run.Step) {
	if in.OnStepStart != nil {
		in.OnStepStart(step)
	}
}

func (in RunJobInput) stepEnded(result run.StepResult) {
	if in.OnStepEnd != nil {
		in.OnStepEnd(result)
	}
}

// RunJob reproduces one workflow job locally: resolve it, run its steps in a
// single container, and capture the context of the first failure.
//
// It is the operation behind both `abel run <job>` and the `run_job` MCP tool.
// Neither transport holds any of this logic; that is what makes the two
// surfaces impossible to drift apart.
type RunJob struct {
	workflows Workflows
	runner    run.Runner
	failures  FailureStore
	clock     run.Clock
}

// NewRunJob builds the use-case. Every dependency is required; a missing one is
// a compile error, which is the point of wiring by hand.
func NewRunJob(workflows Workflows, runner run.Runner, failures FailureStore, clock run.Clock) *RunJob {
	return &RunJob{workflows: workflows, runner: runner, failures: failures, clock: clock}
}

// Plan resolves a job without running anything. `abel run --dry-run` uses it,
// and it is the cheap way to surface resolution warnings before pulling an
// image.
func (u *RunJob) Plan(ctx context.Context, jobID string, opts workflow.Options) (run.Plan, error) {
	files, err := u.workflows.Load(ctx)
	if err != nil {
		return run.Plan{}, err
	}
	file, err := findJob(files, jobID)
	if err != nil {
		return run.Plan{}, err
	}
	return workflow.Resolve(file, jobID, opts)
}

// Execute runs the job.
//
// A failing step is a normal outcome, reported in Result.Failure with a nil
// error — abel did its job. A non-nil error means abel could not run the job at
// all. Callers must branch on Result.OK, not on err, to decide whether CI would
// have gone green.
func (u *RunJob) Execute(ctx context.Context, in RunJobInput) (run.Result, error) {
	plan, err := u.Plan(ctx, in.JobID, in.Resolve)
	if err != nil {
		return run.Result{}, err
	}
	if !plan.Runnable() {
		return run.Result{}, errs.New(errs.KindUnsupported, opRunJob,
			"job %q has no `run:` steps for abel to execute", in.JobID).With("job", in.JobID)
	}

	session, err := u.runner.Start(ctx, plan)
	if err != nil {
		return run.Result{}, errs.New(errs.KindOf(err), opRunJob,
			"cannot start a container for job %q", in.JobID).
			With("job", in.JobID).With("image", plan.Image).Wrapping(err)
	}
	defer u.close(ctx, session)

	result, err := u.runSteps(ctx, session, plan, in)
	if err != nil {
		return result, err
	}

	if in.Shell && in.Terminal.usable() {
		if err := session.Attach(ctx, in.Terminal.In, in.Terminal.Out, in.Terminal.Err); err != nil {
			return result, errs.New(errs.KindOf(err), opRunJob,
				"cannot attach a shell to the container for job %q", in.JobID).
				With("job", in.JobID).Wrapping(err)
		}
	}
	return result, nil
}

// runSteps executes the plan's steps in order in one session, stopping at the
// first failure — which is the whole product: the user wants the first thing
// that broke, not a full CI report.
func (u *RunJob) runSteps(
	ctx context.Context, session run.Session, plan run.Plan, in RunJobInput,
) (run.Result, error) {
	tail := run.NewLogTail(in.LogTailLines)
	started := u.clock.Now()
	result := run.Result{
		JobID: plan.JobID,
		Image: plan.Image,
		Steps: make([]run.StepResult, 0, len(plan.Steps)),
	}

	for _, step := range plan.Steps {
		if step.Skip {
			skipped := run.StepResult{Step: step, Skipped: true}
			result.Steps = append(result.Steps, skipped)
			in.stepEnded(skipped)
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, errs.New(errs.KindCancelled, opRunJob,
				"run cancelled before step %d (%s)", step.Index+1, step.Name).Wrapping(err)
		}

		in.stepStarted(step)
		tail.Reset()
		out := &lineTee{out: in.Logs, tail: tail}
		stepStarted := u.clock.Now()
		exitCode, err := session.Exec(ctx, step, out)
		out.flush()
		duration := u.clock.Now().Sub(stepStarted)

		if err != nil {
			return result, errs.New(errs.KindOf(err), opRunJob,
				"step %d (%s) could not be executed", step.Index+1, step.Name).
				With("job", plan.JobID).With("step", step.Name).Wrapping(err)
		}

		stepResult := run.StepResult{Step: step, ExitCode: exitCode, Duration: duration}
		result.Steps = append(result.Steps, stepResult)
		in.stepEnded(stepResult)

		if exitCode != 0 {
			failure := run.CaptureFailure(plan, step, exitCode, tail.Lines(), u.clock.Now()).Redact()
			result.Failure = &failure
			break
		}
	}
	result.Duration = u.clock.Now().Sub(started)

	return result, u.record(ctx, result)
}

// record persists or clears the job's failure. A green run deletes the stored
// failure so an agent polling `get_failure` cannot act on a stale one.
//
// A store error is returned, but the Result stays populated and valid: the run
// really did happen, and the caller should still show it.
func (u *RunJob) record(ctx context.Context, result run.Result) error {
	if result.Failure == nil {
		if err := u.failures.Delete(ctx, result.JobID); err != nil {
			return errs.New(errs.KindOf(err), opRunJob,
				"job %q passed, but the previous failure record could not be cleared", result.JobID).
				With("job", result.JobID).Wrapping(err)
		}
		return nil
	}
	if err := u.failures.Put(ctx, *result.Failure); err != nil {
		return errs.New(errs.KindOf(err), opRunJob,
			"job %q failed, but the failure context could not be stored", result.JobID).
			With("job", result.JobID).Wrapping(err)
	}
	return nil
}

// close removes the container without letting a cleanup failure mask the run's
// outcome.
func (u *RunJob) close(ctx context.Context, session run.Session) {
	// Cleanup must survive the cancellation that triggered it (Ctrl-C), or the
	// container leaks exactly when the user is most likely to notice.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_ = session.Close(cleanupCtx) //nolint:errcheck // a leaked container must not replace the run's result
}

// lineTee streams container output to the user verbatim while feeding whole
// lines into the bounded tail a failure is captured from.
type lineTee struct {
	out  io.Writer
	tail *run.LogTail
	buf  strings.Builder
}

func (t *lineTee) Write(p []byte) (int, error) {
	if t.out != nil {
		if _, err := t.out.Write(p); err != nil {
			return 0, err
		}
	}
	t.buf.Write(p)
	t.drainCompleteLines()
	return len(p), nil
}

func (t *lineTee) drainCompleteLines() {
	rest := t.buf.String()
	for {
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			break
		}
		t.tail.Add(strings.TrimRight(rest[:idx], "\r"))
		rest = rest[idx+1:]
	}
	t.buf.Reset()
	t.buf.WriteString(rest)
}

// flush records a trailing line that never got its newline — the usual shape of
// a crash message, and the one you least want to lose.
func (t *lineTee) flush() {
	if rest := strings.TrimRight(t.buf.String(), "\r"); rest != "" {
		t.tail.Add(rest)
	}
	t.buf.Reset()
}
