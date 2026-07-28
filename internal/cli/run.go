package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
	"github.com/elliot14A/abel/internal/infra/agent"
)

type runCmd struct {
	Job string `arg:"" help:"Workflow job to reproduce."`

	Shell  bool   `help:"Drop into a shell in the container when the run finishes or a step fails."`
	Fix    string `help:"On failure, hand the failure context to this agent command, then re-run once." placeholder:"COMMAND"`
	DryRun bool   `help:"Resolve the job and print the plan without running anything."`
	Image  string `help:"Override the container image for every step." placeholder:"IMAGE"`
	Pull   bool   `help:"Pull the image even if it is already present locally."`
	Tail   int    `help:"How many log lines to keep in the captured failure." default:"200"`
	JSON   bool   `help:"Print the result as JSON instead of a human report."`
}

func (c *runCmd) Run(ctx context.Context, deps *deps) error {
	opts := workflow.Options{Image: c.Image}

	if c.DryRun {
		return c.dryRun(ctx, deps, opts)
	}

	uc, err := deps.runJob(ctx, c.Pull, deps.stdio.Err)
	if err != nil {
		return err
	}

	result, err := c.execute(ctx, deps, uc, opts)
	if err != nil {
		return err
	}
	if result.OK() {
		return nil
	}

	if c.Fix != "" {
		return c.fixAndRerun(ctx, deps, uc, opts, *result.Failure)
	}
	// A failing step is not an abel error; it is the answer to the question the
	// user asked. Report it through the exit code, not through err.
	deps.exitCode = ExitStepFailed
	return nil
}

func (c *runCmd) dryRun(ctx context.Context, deps *deps, opts workflow.Options) error {
	// Planning needs no daemon, so --dry-run works with Docker stopped.
	plan, err := app.NewRunJob(deps.workflows, nil, deps.failures, deps.clock).Plan(ctx, c.Job, opts)
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(deps.stdio.Out, plan)
	}
	fmt.Fprint(deps.stdio.Out, deps.ui.PlanHeader(plan))
	for _, step := range plan.Steps {
		fmt.Fprint(deps.stdio.Out, deps.ui.PlannedStep(step))
	}
	return nil
}

// execute runs the job once and prints the report.
func (c *runCmd) execute(
	ctx context.Context, deps *deps, uc *app.RunJob, opts workflow.Options,
) (run.Result, error) {
	plan, err := uc.Plan(ctx, c.Job, opts)
	if err != nil {
		return run.Result{}, err
	}
	if !c.JSON {
		fmt.Fprint(deps.stdio.Out, deps.ui.PlanHeader(plan))
	}

	in := app.RunJobInput{
		JobID:        c.Job,
		Logs:         c.logSink(deps),
		Shell:        c.Shell,
		Resolve:      opts,
		LogTailLines: c.Tail,
	}
	if !c.JSON {
		// Print each step's heading before its output and its verdict after, so
		// a slow step looks like work rather than a hang.
		in.OnStepStart = func(step run.Step) {
			fmt.Fprint(deps.stdio.Out, deps.ui.StepStart(step))
		}
		in.OnStepEnd = func(result run.StepResult) {
			fmt.Fprint(deps.stdio.Out, deps.ui.StepResult(result))
		}
	}
	if c.Shell {
		in.Terminal = app.Terminal{In: deps.stdio.In, Out: deps.stdio.Out, Err: deps.stdio.Err}
	}

	result, err := uc.Execute(ctx, in)
	if err != nil {
		return result, err
	}

	if c.JSON {
		return result, writeJSON(deps.stdio.Out, result)
	}
	fmt.Fprint(deps.stdio.Out, deps.ui.Summary(result))
	if result.Failure != nil {
		fmt.Fprint(deps.stdio.Out, deps.ui.Failure(*result.Failure))
	}
	return result, nil
}

// logSink is where the container's live output goes. With --json it is
// discarded so that stdout carries nothing but the JSON document.
func (c *runCmd) logSink(deps *deps) io.Writer {
	if c.JSON {
		return nil
	}
	return deps.stdio.Out
}

// fixAndRerun is the assisted half of the loop: hand the failure to the agent,
// then re-run once to find out whether it actually worked. abel does not loop
// further and does not commit anything — the developer reviews the diff.
func (c *runCmd) fixAndRerun(
	ctx context.Context, deps *deps, uc *app.RunJob, opts workflow.Options, failure run.Failure,
) error {
	fixer, err := agent.New(agent.Config{Command: c.Fix, Dir: deps.repoRoot})
	if err != nil {
		return err
	}

	fmt.Fprintf(deps.stdio.Out, "\nhanding the failure to %q…\n", c.Fix)
	if err := fixer.Fix(ctx, failure, deps.stdio.Out); err != nil {
		return err
	}

	fmt.Fprintf(deps.stdio.Out, "\nre-running %s…\n", c.Job)
	result, err := c.execute(ctx, deps, uc, opts)
	if err != nil {
		return err
	}
	if !result.OK() {
		deps.exitCode = ExitStepFailed
		fmt.Fprintf(deps.stdio.Err,
			"\nthe agent's change did not fix %s — review the diff and try again\n", c.Job)
		return nil
	}
	fmt.Fprintf(deps.stdio.Out, "\n%s passes now — review the diff before you commit\n", c.Job)
	return nil
}
