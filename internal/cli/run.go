package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
	"github.com/elliot14A/abel/internal/infra/agent"
)

type runCmd struct {
	Job     string        `arg:"" help:"Workflow job to reproduce."`
	Shell   bool          `help:"Drop into a shell in the container when the run finishes or a step fails."`
	Fix     string        `help:"On failure, hand the failure context to this agent command, then re-run once." placeholder:"COMMAND"`
	DryRun  bool          `help:"Resolve the job and print the plan without running anything."`
	Image   string        `help:"Override the container image for every step." placeholder:"IMAGE"`
	Pull    bool          `help:"Pull the image even if it is already present locally."`
	Tail    int           `help:"How many log lines to keep in the captured failure." default:"200"`
	Timeout time.Duration `help:"Abandon the run if it takes longer than this. Unset means no limit." placeholder:"DURATION"`
	JSON    bool          `help:"Print the result as JSON instead of a human report."`
}

func (c *runCmd) Run(ctx context.Context, a *abel) error {
	opts := workflow.Options{Image: c.Image}

	if c.DryRun {
		return c.dryRun(ctx, a, opts)
	}

	var progress *pullPrinter
	if !c.JSON {
		progress = a.pullProgress()
	}

	uc, err := a.runJob(ctx, c.Pull, progress)
	if err != nil {
		return err
	}

	if c.Timeout > 0 {
		timed, cancel := context.WithTimeout(ctx, c.Timeout)
		defer cancel()
		ctx = timed
	}

	result, err := c.execute(ctx, a, uc, opts)
	if err != nil {
		return err
	}
	if result.OK() {
		return nil
	}

	if c.Fix != "" {
		return c.fixAndRerun(ctx, a, uc, opts, *result.Failure)
	}

	a.exitCode = ExitStepFailed
	return nil
}

func (c *runCmd) dryRun(ctx context.Context, a *abel, opts workflow.Options) error {
	plan, err := app.NewRunJob(a.workflows, nil, a.failures, a.clock, a.log).Plan(ctx, c.Job, opts)
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(a.stdio.Out, plan)
	}
	fmt.Fprint(a.stdio.Out, a.ui.PlanHeader(plan))
	for _, step := range plan.Steps {
		fmt.Fprint(a.stdio.Out, a.ui.PlannedStep(step))
	}
	return nil
}

func (c *runCmd) execute(
	ctx context.Context, a *abel, uc *app.RunJob, opts workflow.Options,
) (run.Result, error) {
	plan, err := uc.Plan(ctx, c.Job, opts)
	if err != nil {
		return run.Result{}, err
	}
	if !c.JSON {
		fmt.Fprint(a.stdio.Out, a.ui.PlanHeader(plan))
	}

	in := app.RunJobInput{
		JobID:        c.Job,
		Logs:         c.logSink(a),
		Shell:        c.Shell,
		Resolve:      opts,
		LogTailLines: c.Tail,
	}
	if !c.JSON {
		in.OnStepStart = func(step run.Step) {
			fmt.Fprint(a.stdio.Out, a.ui.StepStart(step))
		}
		in.OnStepEnd = func(result run.StepResult) {
			fmt.Fprint(a.stdio.Out, a.ui.StepResult(result))
		}
	}
	if c.Shell {
		in.Terminal = app.Terminal{In: a.stdio.In, Out: a.stdio.Out, Err: a.stdio.Err}
	}

	result, err := uc.Execute(ctx, in)
	if err != nil {
		return result, err
	}

	if c.JSON {
		return result, writeJSON(a.stdio.Out, result)
	}
	fmt.Fprint(a.stdio.Out, a.ui.Summary(result))
	if result.Failure != nil {
		fmt.Fprint(a.stdio.Out, a.ui.Failure(*result.Failure))
	}
	return result, nil
}

func (c *runCmd) logSink(a *abel) io.Writer {
	if c.JSON {
		return nil
	}
	return a.stdio.Out
}

func (c *runCmd) fixAndRerun(
	ctx context.Context, a *abel, uc *app.RunJob, opts workflow.Options, failure run.Failure,
) error {
	fixer, err := agent.New(agent.Config{Command: c.Fix, Dir: a.repoRoot})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.stdio.Out, "\nhanding the failure to %q…\n", c.Fix)
	if err := fixer.Fix(ctx, failure, a.stdio.Out); err != nil {
		return err
	}

	fmt.Fprintf(a.stdio.Out, "\nre-running %s…\n", c.Job)
	result, err := c.execute(ctx, a, uc, opts)
	if err != nil {
		return err
	}
	if !result.OK() {
		a.exitCode = ExitStepFailed
		fmt.Fprintf(a.stdio.Err,
			"\nthe agent's change did not fix %s; review the diff and try again\n", c.Job)
		return nil
	}
	fmt.Fprintf(a.stdio.Out, "\n%s passes now; review the diff before you commit\n", c.Job)
	return nil
}
