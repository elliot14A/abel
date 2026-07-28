package run

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// DefaultLogTailLines is how many trailing log lines a failure keeps. Enough
// for a stack trace or a compiler's error block, small enough that an agent
// reading it over MCP does not drown.
const DefaultLogTailLines = 200

// Redacted is what a secret's value is replaced with everywhere it appears.
const Redacted = "***"

// Failure is the context abel captures when a step fails: everything needed to
// understand the failure without re-running it. It is the payload of the
// `get_failure` MCP tool and the input to `--fix`, so every field must be safe
// to hand to a third-party agent — see [Failure.Redact].
type Failure struct {
	JobID      string            `json:"job_id"`
	JobName    string            `json:"job_name"`
	Image      string            `json:"image"`
	StepIndex  int               `json:"step_index"`
	StepName   string            `json:"step_name"`
	Command    string            `json:"command"`
	ExitCode   int               `json:"exit_code"`
	LogTail    []string          `json:"log_tail"`
	Env        map[string]string `json:"env"`
	WorkDir    string            `json:"working_directory"`
	Source     string            `json:"workflow_path"`
	Line       int               `json:"workflow_line"`
	CapturedAt time.Time         `json:"captured_at"`
	// Fixed is set by the `mark_fixed` tool. A fixed failure is kept rather
	// than deleted so that a re-run can be compared against it.
	Fixed bool `json:"fixed"`
}

// CaptureFailure builds a Failure from a step that exited non-zero. It copies
// every map and slice it is handed: the caller owns a live log buffer and a
// mutable env, and a captured failure must not change underneath the store.
func CaptureFailure(plan Plan, step Step, exitCode int, logTail []string, now time.Time) *Failure {
	return &Failure{
		JobID:      plan.JobID,
		JobName:    plan.JobName,
		Image:      plan.Image,
		StepIndex:  step.Index,
		StepName:   step.Name,
		Command:    step.Script,
		ExitCode:   exitCode,
		LogTail:    slices.Clone(logTail),
		Env:        maps.Clone(step.Env),
		WorkDir:    step.WorkingDir,
		Source:     plan.Source,
		Line:       step.SourceLine,
		CapturedAt: now,
	}
}

// Redact replaces the value of every environment variable whose name looks
// secret, and scrubs those same values wherever they appear in the captured
// logs. It returns a new Failure; the receiver is untouched.
//
// This runs before a failure is stored, printed or served, so a token that
// leaked into a build log does not leak on to an agent.
func (f Failure) Redact() Failure {
	out := f
	out.Env = make(map[string]string, len(f.Env))

	secrets := make([]string, 0, len(f.Env))
	for k, v := range f.Env {
		if IsSecretName(k) && v != "" {
			out.Env[k] = Redacted
			secrets = append(secrets, v)
			continue
		}
		out.Env[k] = v
	}
	// Longest first, so an overlapping shorter secret cannot leave a fragment.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })

	out.LogTail = make([]string, len(f.LogTail))
	for i, line := range f.LogTail {
		for _, secret := range secrets {
			line = strings.ReplaceAll(line, secret, Redacted)
		}
		out.LogTail[i] = line
	}
	out.Command = f.Command
	for _, secret := range secrets {
		out.Command = strings.ReplaceAll(out.Command, secret, Redacted)
	}
	return out
}

// secretNameFragments are matched case-insensitively against variable names.
// The list is deliberately broad: a false positive costs an agent one masked
// value, a false negative leaks a credential.
var secretNameFragments = []string{
	"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
	"PRIVATE_KEY", "APIKEY", "API_KEY", "ACCESS_KEY", "SESSION", "AUTH",
}

// IsSecretName reports whether an environment variable name looks like it
// holds a credential.
func IsSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, fragment := range secretNameFragments {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

// LogTail is a fixed-capacity ring buffer of the most recent log lines. The
// runner writes every line of container output through it, so memory stays
// bounded no matter how noisy a step is, and the failure context always holds
// the lines nearest the error.
//
// The zero value is unusable; construct with [NewLogTail].
type LogTail struct {
	lines []string
	next  int
	full  bool
	// total counts every line ever written, so the report can say how much
	// output was dropped rather than pretending the tail is the whole log.
	total int
}

// NewLogTail returns a tail keeping at most capacity lines. A non-positive
// capacity falls back to DefaultLogTailLines.
func NewLogTail(capacity int) *LogTail {
	if capacity <= 0 {
		capacity = DefaultLogTailLines
	}
	return &LogTail{lines: make([]string, capacity)}
}

// Add records one line.
func (t *LogTail) Add(line string) {
	t.lines[t.next] = line
	t.next = (t.next + 1) % len(t.lines)
	if t.next == 0 {
		t.full = true
	}
	t.total++
}

// AddAll records several lines.
func (t *LogTail) AddAll(lines []string) {
	for _, l := range lines {
		t.Add(l)
	}
}

// Lines returns the retained lines in chronological order.
func (t *LogTail) Lines() []string {
	if !t.full {
		return slices.Clone(t.lines[:t.next])
	}
	out := make([]string, 0, len(t.lines))
	out = append(out, t.lines[t.next:]...)
	return append(out, t.lines[:t.next]...)
}

// Dropped reports how many lines fell out of the window.
func (t *LogTail) Dropped() int {
	if !t.full {
		return 0
	}
	return t.total - len(t.lines)
}

// Reset clears the buffer for reuse between steps.
func (t *LogTail) Reset() {
	t.next, t.full, t.total = 0, false, 0
}
