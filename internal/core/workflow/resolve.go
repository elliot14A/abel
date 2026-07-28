package workflow

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opResolve = "workflow.Resolve"

// RunnerImages maps `runs-on:` labels to the container image abel substitutes.
// The Ubuntu images are the community act images, which carry the toolchains a
// GitHub-hosted runner has; a bare ubuntu image would fail on the first
// `setup-node`-shaped assumption.
//
// A label absent from this map is not an error if the job also declares
// `container:` — an explicit image always wins.
var RunnerImages = map[string]string{
	"ubuntu-latest": "catthehacker/ubuntu:act-latest",
	"ubuntu-24.04":  "catthehacker/ubuntu:act-24.04",
	"ubuntu-22.04":  "catthehacker/ubuntu:act-22.04",
	"ubuntu-20.04":  "catthehacker/ubuntu:act-20.04",
}

// supportedShells are the shells abel knows how to invoke. GitHub supports
// python, pwsh and cmd as well; abel refuses them rather than guessing.
var supportedShells = []string{"bash", "sh"}

// Options tunes resolution. The zero value is valid and uses the defaults.
type Options struct {
	// Workdir overrides the in-container mount point of the repository.
	Workdir string
	// Image overrides the image for every job, ignoring `runs-on:` and
	// `container:`. This is what `abel run --image` sets.
	Image string
	// RunnerImages overrides the label-to-image map.
	RunnerImages map[string]string
}

func (o Options) workdir() string {
	if o.Workdir != "" {
		return o.Workdir
	}
	return run.DefaultWorkdir
}

func (o Options) runnerImages() map[string]string {
	if o.RunnerImages != nil {
		return o.RunnerImages
	}
	return RunnerImages
}

// Resolve turns one job of a workflow into a runnable plan, merging the three
// layers of env and defaults, choosing an image, and classifying every step as
// runnable or skipped.
//
// Resolution never silently drops a feature: anything abel does not honour
// becomes a [run.Warning] on the plan.
func Resolve(f File, jobID string, opts Options) (run.Plan, error) {
	job, ok := f.Job(jobID)
	if !ok {
		return run.Plan{}, errs.New(errs.KindNotFound, opResolve,
			"no job %q in %s (available: %s)", jobID, f.Path, strings.Join(f.JobIDs, ", ")).
			With("job", jobID)
	}

	image, err := resolveImage(job, opts)
	if err != nil {
		return run.Plan{}, err
	}

	workdir := opts.workdir()
	plan := run.Plan{
		JobID:    job.ID,
		JobName:  job.Name,
		Source:   f.Path,
		Image:    image,
		Workdir:  workdir,
		Steps:    make([]run.Step, 0, len(job.Steps)),
		Warnings: jobWarnings(job),
	}

	baseEnv := mergeEnv(f.Env, job.Env, job.Container.Env)
	shell := firstNonEmpty(job.Defaults.Shell, f.Defaults.Shell, "bash")
	baseDir := firstNonEmpty(job.Defaults.WorkingDirectory, f.Defaults.WorkingDirectory)

	for i, step := range job.Steps {
		resolved, warns, err := resolveStep(step, i, baseEnv, shell, baseDir, workdir)
		if err != nil {
			return run.Plan{}, err
		}
		plan.Steps = append(plan.Steps, resolved)
		plan.Warnings = append(plan.Warnings, warns...)
	}

	if len(plan.Steps) == 0 {
		return run.Plan{}, errs.New(errs.KindValidation, opResolve,
			"job %q has no steps", jobID).With("job", jobID)
	}
	return plan, nil
}

func resolveImage(job Job, opts Options) (string, error) {
	if opts.Image != "" {
		return opts.Image, nil
	}
	if job.Container.Image != "" {
		return job.Container.Image, nil
	}

	images := opts.runnerImages()
	for _, label := range job.RunsOn {
		if image, ok := images[label]; ok {
			return image, nil
		}
	}

	switch {
	case len(job.RunsOn) == 0:
		return "", errs.New(errs.KindValidation, opResolve,
			"job %q declares neither runs-on nor container", job.ID).
			With("job", job.ID).With("line", fmt.Sprint(job.Line))
	default:
		return "", errs.New(errs.KindUnsupported, opResolve,
			"job %q runs on %q, which abel has no local image for — "+
				"add `container:` to the job or pass --image",
			job.ID, strings.Join(job.RunsOn, ", ")).
			With("job", job.ID).With("line", fmt.Sprint(job.Line))
	}
}

func jobWarnings(job Job) []run.Warning {
	var warnings []run.Warning
	if job.HasStrategy {
		warnings = append(warnings, run.Warning{
			SourceLine: job.Line,
			Message:    "matrix strategy ignored; abel runs the job once with no matrix values",
		})
	}
	if job.If != "" {
		warnings = append(warnings, run.Warning{
			SourceLine: job.Line,
			Message:    "job-level `if:` is not evaluated; the job runs unconditionally",
		})
	}
	if len(job.Needs) > 0 {
		warnings = append(warnings, run.Warning{
			SourceLine: job.Line,
			Message: fmt.Sprintf("`needs: %s` is ignored; abel runs one job at a time",
				strings.Join(job.Needs, ", ")),
		})
	}
	if job.Container.Options != "" {
		warnings = append(warnings, run.Warning{
			SourceLine: job.Line,
			Message:    "`container.options` is ignored",
		})
	}
	return warnings
}

func resolveStep(
	step Step, index int, baseEnv map[string]string, baseShell, baseDir, workdir string,
) (run.Step, []run.Warning, error) {
	out := run.Step{
		Index:      index,
		Name:       step.Label(),
		Env:        mergeEnv(baseEnv, step.Env),
		SourceLine: step.Line,
		WorkingDir: containerPath(workdir, firstNonEmpty(step.WorkingDirectory, baseDir)),
	}

	if !step.IsRun() {
		out.Skip, out.SkipReason = true, skipReasonFor(step)
		return out, []run.Warning{{SourceLine: step.Line, Message: out.SkipReason}}, nil
	}

	shell := firstNonEmpty(step.Shell, baseShell)
	if !slices.Contains(supportedShells, shell) {
		return run.Step{}, nil, errs.New(errs.KindUnsupported, opResolve,
			"step %d uses shell %q; abel supports %s",
			index+1, shell, strings.Join(supportedShells, " and ")).
			With("step", fmt.Sprint(index)).With("line", fmt.Sprint(step.Line))
	}
	out.Shell, out.Script = shell, step.Run

	var warnings []run.Warning
	if step.If != "" {
		warnings = append(warnings, run.Warning{
			SourceLine: step.Line,
			Message:    fmt.Sprintf("step %d: `if:` is not evaluated; the step runs unconditionally", index+1),
		})
	}
	if strings.Contains(step.Run, "${{") {
		warnings = append(warnings, run.Warning{
			SourceLine: step.Line,
			Message: fmt.Sprintf(
				"step %d: `${{ }}` expressions are passed through unevaluated", index+1),
		})
	}
	return out, warnings, nil
}

// skipReasonFor explains, in the user's terms, why abel is not running a
// `uses:` step. abel's scope is `run:` steps; the honest move is to say what it
// skipped and why, not to half-emulate the action.
func skipReasonFor(step Step) string {
	action, _, _ := strings.Cut(step.Uses, "@")
	switch {
	case step.Uses == "":
		return fmt.Sprintf("step %q has neither `run:` nor `uses:`", step.Label())
	case action == "actions/checkout":
		return "skipped `actions/checkout`: your working tree is already mounted"
	case strings.HasPrefix(action, "actions/setup-"):
		return fmt.Sprintf(
			"skipped %q: abel does not provision toolchains — use a `container:` image that has it",
			step.Uses)
	case strings.HasPrefix(action, "actions/cache"):
		return fmt.Sprintf("skipped %q: caching is a no-op locally", step.Uses)
	default:
		return fmt.Sprintf("skipped %q: abel runs `run:` steps only", step.Uses)
	}
}

// containerPath resolves a workflow's working-directory (repo-relative, or
// absolute) against the container mount point.
func containerPath(workdir, dir string) string {
	switch {
	case dir == "":
		return workdir
	case path.IsAbs(dir):
		return path.Clean(dir)
	default:
		return path.Join(workdir, dir)
	}
}

// mergeEnv layers environment maps left to right, later winning. It always
// returns a fresh map, so no caller shares state with the workflow file.
func mergeEnv(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
