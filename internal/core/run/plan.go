// Package run holds the execution model: what abel is about to do (a [Plan]),
// what happened (a [Result]), and what to hand an agent when it goes wrong (a
// [Failure]).
//
// Like every core package it is pure. It describes a container run without
// knowing that Docker exists; the ports in ports.go are the seam.
package run

import (
	"fmt"
	"strings"
	"time"
)

// DefaultWorkdir is where abel mounts the repository inside the container.
// GitHub uses /github/workspace; abel uses a neutral path so that a workflow
// depending on the former fails loudly rather than subtly.
const DefaultWorkdir = "/abel/workspace"

// Plan is a fully resolved, runnable job: everything needed to execute it, with
// no further reference to the workflow file.
type Plan struct {
	// JobID is the workflow's key for this job.
	JobID string
	// JobName is the display name.
	JobName string
	// Source is the workflow file the job came from, carried through so a
	// failure can point back at it.
	Source string
	// Image is the container image to run the steps in.
	Image string
	// Workdir is the in-container mount point of the repository.
	Workdir string
	// Steps are in execution order, including steps marked Skip — a skipped
	// step is still reported, so the user can see what abel chose not to do.
	Steps []Step
	// Warnings records everything abel deliberately did not honour: matrices,
	// conditions, unsupported actions. Surfacing these is the difference
	// between "a fast debug loop" and "a CI emulator that lies".
	Warnings []Warning
}

// Runnable reports whether the plan has at least one step abel will execute.
func (p Plan) Runnable() bool {
	for _, s := range p.Steps {
		if !s.Skip {
			return true
		}
	}
	return false
}

// Step is one resolved command to execute in the container.
type Step struct {
	// Index is the step's 0-based position in the job, stable across skips so
	// that "step 3" means the same thing in the CLI, the logs and MCP.
	Index int
	// Name is the display label.
	Name string
	// Script is the shell script body, exactly as written in `run:`.
	Script string
	// Shell is the shell to execute Script with ("bash" or "sh").
	Shell string
	// Env is the fully merged environment for this step.
	Env map[string]string
	// WorkingDir is the absolute in-container directory to run in.
	WorkingDir string
	// Skip marks a step abel will not execute; SkipReason says why, in words
	// meant for the user.
	Skip       bool
	SkipReason string
	// SourceLine is the step's line in the workflow file.
	SourceLine int
}

// Command returns the argv abel executes for this step. Bash runs with -e so a
// failing line fails the step, matching GitHub's default shell behaviour.
func (s Step) Command() []string {
	switch s.Shell {
	case "sh":
		return []string{"sh", "-e", "-c", s.Script}
	default:
		return []string{"bash", "-e", "-o", "pipefail", "-c", s.Script}
	}
}

// Warning is something abel noticed and chose not to act on.
type Warning struct {
	// SourceLine is the workflow line the warning refers to, or 0 if none.
	SourceLine int
	Message    string
}

func (w Warning) String() string {
	if w.SourceLine > 0 {
		return fmt.Sprintf("line %d: %s", w.SourceLine, w.Message)
	}
	return w.Message
}

// StepResult is the outcome of one step.
type StepResult struct {
	Step     Step
	ExitCode int
	Duration time.Duration
	// Skipped mirrors Step.Skip; it is repeated here so a Result is
	// self-describing when serialised on its own.
	Skipped bool
}

// OK reports whether the step succeeded or was skipped.
func (r StepResult) OK() bool { return r.Skipped || r.ExitCode == 0 }

// Result is the outcome of a whole job.
type Result struct {
	JobID    string
	Image    string
	Steps    []StepResult
	Duration time.Duration
	// Failure is set exactly when a step exited non-zero. It is the payload
	// abel serves over MCP.
	Failure *Failure
}

// OK reports whether the job completed with every executed step succeeding.
func (r Result) OK() bool { return r.Failure == nil }

// Summary is a one-line human summary of the run.
func (r Result) Summary() string {
	var ran, skipped int
	for _, s := range r.Steps {
		if s.Skipped {
			skipped++
		} else {
			ran++
		}
	}
	status := "passed"
	if r.Failure != nil {
		status = fmt.Sprintf("failed at step %d (%s)", r.Failure.StepIndex+1, r.Failure.StepName)
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s %s in %s — %d step(s) run", r.JobID, status, r.Duration.Round(time.Millisecond), ran)
	if skipped > 0 {
		fmt.Fprintf(b, ", %d skipped", skipped)
	}
	return b.String()
}
