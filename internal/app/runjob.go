package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

const opRunJob = "app.RunJob"

const cleanupTimeout = 30 * time.Second

type Terminal struct {
	In       io.Reader
	Out, Err io.Writer
}

func (t Terminal) usable() bool { return t.In != nil && t.Out != nil }

type RunJobInput struct {
	JobID         string
	Logs          io.Writer
	Shell         bool
	Terminal      Terminal
	Resolve       workflow.Options
	LogTailLines  int
	CaptureOutput bool
	OnStepStart   func(run.Step)
	OnStepEnd     func(run.StepResult)
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

type RunJob struct {
	workflows Workflows
	runner    run.Runner
	failures  FailureStore
	clock     run.Clock
	log       *slog.Logger
}

func NewRunJob(
	workflows Workflows, runner run.Runner, failures FailureStore, clock run.Clock, log *slog.Logger,
) *RunJob {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &RunJob{workflows: workflows, runner: runner, failures: failures, clock: clock, log: log}
}

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

func (u *RunJob) Execute(ctx context.Context, in RunJobInput) (run.Result, error) {
	plan, err := u.Plan(ctx, in.JobID, in.Resolve)
	if err != nil {
		return run.Result{}, err
	}
	if !plan.Runnable() {
		return run.Result{}, errs.New(errs.KindUnsupported, opRunJob,
			"job %q has no `run:` steps for abel to execute", in.JobID).With("job", in.JobID)
	}

	u.log.Info("run_start",
		"job", plan.JobID,
		"image", plan.Image,
		"source", plan.Source,
		"steps", len(plan.Steps),
		"warnings", len(plan.Warnings))
	for _, w := range plan.Warnings {
		u.log.Warn("plan_warning", "job", plan.JobID, "line", w.SourceLine, "message", w.Message)
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
			u.log.Debug("step_skipped",
				"job", plan.JobID, "step", step.Index+1,
				"name", run.RedactText(step.Name, step.Env), "reason", step.SkipReason)
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, errs.New(errs.KindCancelled, opRunJob,
				"run cancelled before step %d (%s)", step.Index+1, step.Name).Wrapping(err)
		}

		in.stepStarted(step)
		name := run.RedactText(step.Name, step.Env)
		u.log.Debug("step_start",
			"job", plan.JobID, "step", step.Index+1, "name", name,
			"shell", step.Shell, "dir", step.WorkingDir)

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
		if in.CaptureOutput {
			stepResult.Output = run.RedactLines(tail.Lines(), step.Env)
		}
		result.Steps = append(result.Steps, stepResult)
		in.stepEnded(stepResult)
		u.log.Debug("step_end",
			"job", plan.JobID, "step", step.Index+1, "name", name,
			"exit", exitCode, "ms", duration.Milliseconds())

		if exitCode != 0 {
			failure := run.CaptureFailure(plan, step, exitCode, tail, u.clock.Now()).Redact()
			result.Failure = &failure
			u.log.Error("failure_captured",
				"job", plan.JobID, "step", step.Index+1, "name", failure.StepName,
				"exit", exitCode, "source", failure.Source, "line", failure.Line)
			break
		}
	}
	result.Duration = u.clock.Now().Sub(started)
	u.log.Info("run_end",
		"job", plan.JobID, "passed", result.OK(), "ms", result.Duration.Milliseconds())

	return result, u.record(ctx, result)
}

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

func (u *RunJob) close(ctx context.Context, session run.Session) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_ = session.Close(cleanupCtx) //nolint:errcheck // a leaked container must not replace the run's result
}

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

func (t *lineTee) flush() {
	if rest := strings.TrimRight(t.buf.String(), "\r"); rest != "" {
		t.tail.Add(rest)
	}
	t.buf.Reset()
}
