package run

import (
	"fmt"
	"strings"
	"time"
)

const DefaultWorkdir = "/abel/workspace"

type Plan struct {
	JobID    string
	JobName  string
	Source   string
	Image    string
	Workdir  string
	Steps    []Step
	Warnings []Warning
}

func (p Plan) Runnable() bool {
	for _, s := range p.Steps {
		if !s.Skip {
			return true
		}
	}
	return false
}

type Step struct {
	Index      int
	Name       string
	Script     string
	Shell      string
	Env        map[string]string
	WorkingDir string
	Skip       bool
	SkipReason string
	SourceLine int
}

func (s Step) Command() []string {
	switch s.Shell {
	case "sh":
		return []string{"sh", "-e", "-c", s.Script}
	default:
		return []string{"bash", "-e", "-o", "pipefail", "-c", s.Script}
	}
}

type Warning struct {
	SourceLine int
	Message    string
}

func (w Warning) String() string {
	if w.SourceLine > 0 {
		return fmt.Sprintf("line %d: %s", w.SourceLine, w.Message)
	}
	return w.Message
}

type StepResult struct {
	Step     Step
	ExitCode int
	Duration time.Duration
	Skipped  bool
	Output   []string
}

func (r StepResult) OK() bool { return r.Skipped || r.ExitCode == 0 }

type Result struct {
	JobID    string
	Image    string
	Steps    []StepResult
	Duration time.Duration
	Failure  *Failure
}

func (r Result) OK() bool { return r.Failure == nil }

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
	fmt.Fprintf(b, "%s %s in %s, %d step(s) run", r.JobID, status, r.Duration.Round(time.Millisecond), ran)
	if skipped > 0 {
		fmt.Fprintf(b, ", %d skipped", skipped)
	}
	return b.String()
}
