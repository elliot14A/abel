package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

type Renderer struct {
	color bool
}

func New(color bool) *Renderer { return &Renderer{color: color} }

func (r *Renderer) style(s lipgloss.Style, text string) string {
	if !r.color {
		return text
	}
	return s.Render(text)
}

var (
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleRedBold = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

func (r *Renderer) PlanHeader(plan run.Plan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s  %s\n",
		r.style(styleCyan, "abel"),
		r.style(styleBold, plan.JobID),
		r.style(styleDim, plan.Image))

	live := 0
	for _, s := range plan.Steps {
		if !s.Skip {
			live++
		}
	}
	fmt.Fprintf(&b, "%s\n", r.style(styleDim,
		fmt.Sprintf("%d step(s) to run, %d skipped, from %s",
			live, len(plan.Steps)-live, plan.Source)))

	for _, w := range plan.Warnings {
		fmt.Fprintf(&b, "%s %s\n", r.style(styleYellow, "!"), r.style(styleDim, w.String()))
	}
	return b.String()
}

func (r *Renderer) StepStart(step run.Step) string {
	return fmt.Sprintf("\n%s %s %s\n",
		r.style(styleCyan, "▸"),
		r.style(styleBold, fmt.Sprintf("step %d", step.Index+1)),
		step.Name)
}

func (r *Renderer) PlannedStep(step run.Step) string {
	if step.Skip {
		return fmt.Sprintf("%s %s %s\n",
			r.style(styleDim, "-"),
			r.style(styleDim, fmt.Sprintf("step %d", step.Index+1)),
			r.style(styleDim, step.SkipReason))
	}
	first := strings.SplitN(step.Script, "\n", 2)[0]
	if strings.Contains(step.Script, "\n") {
		first += " " + ellipsis
	}
	return fmt.Sprintf("%s %s %s\n  %s %s\n",
		r.style(styleCyan, "·"),
		r.style(styleBold, fmt.Sprintf("step %d", step.Index+1)),
		step.Name,
		r.style(styleDim, step.Shell+" in "+step.WorkingDir+":"),
		r.style(styleDim, first))
}

func (r *Renderer) StepResult(result run.StepResult) string {
	switch {
	case result.Skipped:
		return fmt.Sprintf("%s %s %s\n",
			r.style(styleDim, "-"),
			r.style(styleDim, fmt.Sprintf("step %d", result.Step.Index+1)),
			r.style(styleDim, result.Step.SkipReason))
	case result.ExitCode == 0:
		return fmt.Sprintf("%s %s %s\n",
			r.style(styleGreen, "✓"),
			result.Step.Name,
			r.style(styleDim, duration(result.Duration)))
	default:
		return fmt.Sprintf("%s %s %s\n",
			r.style(styleRed, "✗"),
			result.Step.Name,
			r.style(styleDim, fmt.Sprintf("exit %d in %s", result.ExitCode, duration(result.Duration))))
	}
}

func (r *Renderer) Summary(result run.Result) string {
	if result.OK() {
		return fmt.Sprintf("\n%s %s\n", r.style(styleGreen, "PASS"), result.Summary())
	}
	return fmt.Sprintf("\n%s %s\n", r.style(styleRedBold, "FAIL"), result.Summary())
}

func (r *Renderer) Failure(f run.Failure) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n%s\n", r.style(styleRedBold, "failure context"))
	fmt.Fprintf(&b, "  %-9s %s\n", r.style(styleDim, "job"), f.JobID)
	fmt.Fprintf(&b, "  %-9s %d: %s\n", r.style(styleDim, "step"), f.StepIndex+1, f.StepName)
	fmt.Fprintf(&b, "  %-9s %s\n", r.style(styleDim, "command"), indentContinuation(f.Command, 12))
	fmt.Fprintf(&b, "  %-9s %d\n", r.style(styleDim, "exit"), f.ExitCode)
	fmt.Fprintf(&b, "  %-9s %s\n", r.style(styleDim, "image"), f.Image)
	if f.Source != "" {
		fmt.Fprintf(&b, "  %-9s %s:%d\n", r.style(styleDim, "source"), f.Source, f.Line)
	}
	if f.Fixed {
		fmt.Fprintf(&b, "  %-9s %s\n", r.style(styleDim, "claimed"), r.claim(f))
	}

	if len(f.LogTail) > 0 {
		header := fmt.Sprintf("last %d line(s):", len(f.LogTail))
		if f.LogDropped > 0 {
			header = fmt.Sprintf("last %d line(s), %d earlier line(s) dropped:",
				len(f.LogTail), f.LogDropped)
		}
		fmt.Fprintf(&b, "\n  %s\n", r.style(styleDim, header))
		for _, line := range f.LogTail {
			fmt.Fprintf(&b, "  %s %s\n", r.style(styleDim, "│"), line)
		}
	}
	return b.String()
}

func (r *Renderer) Jobs(refs []app.JobRef) string {
	if len(refs) == 0 {
		return "no jobs found\n"
	}
	width := 0
	for _, ref := range refs {
		width = max(width, len(ref.JobID))
	}

	var b strings.Builder
	for _, ref := range refs {
		fmt.Fprintf(&b, "%s  %s\n",
			r.style(styleBold, fmt.Sprintf("%-*s", width, ref.JobID)),
			r.style(styleDim, fmt.Sprintf("%s  (%s)", ref.WorkflowPath, strings.Join(ref.RunsOn, ", "))))
		if !ref.Runnable && ref.Unsupported != "" {
			fmt.Fprintf(&b, "%s %s %s\n",
				strings.Repeat(" ", width),
				r.style(styleYellow, "!"),
				r.style(styleDim, ref.Unsupported))
		}
	}
	return b.String()
}

func (r *Renderer) Error(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", r.style(styleRedBold, "error:"), err.Error())
	if kind := errs.KindOf(err); kind != "" && kind != errs.KindInternal {
		fmt.Fprintf(&b, "%s\n", r.style(styleDim, "  ("+string(kind)+")"))
	}
	return b.String()
}

func (r *Renderer) claim(f run.Failure) string {
	var b strings.Builder
	b.WriteString(r.style(styleYellow, "fixed but not verified"))
	if !f.FixedAt.IsZero() {
		fmt.Fprintf(&b, " %s", r.style(styleDim, f.FixedAt.UTC().Format(time.RFC3339)))
	}
	if f.FixNote != "" {
		fmt.Fprintf(&b, "\n  %-9s %s", "", f.FixNote)
	}
	return b.String()
}

func duration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(10 * time.Millisecond).String()
}

func indentContinuation(s string, indent int) string {
	return strings.ReplaceAll(s, "\n", "\n"+strings.Repeat(" ", indent))
}
