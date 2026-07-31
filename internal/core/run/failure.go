package run

import (
	"maps"
	"slices"
	"strings"
	"time"
)

const DefaultLogTailLines = 200

const Redacted = "***"

type Failure struct {
	JobID      string            `json:"job_id"`
	JobName    string            `json:"job_name"`
	Image      string            `json:"image"`
	StepIndex  int               `json:"step_index"`
	StepName   string            `json:"step_name"`
	Command    string            `json:"command"`
	ExitCode   int               `json:"exit_code"`
	LogTail    []string          `json:"log_tail"`
	LogDropped int               `json:"log_tail_dropped,omitempty"`
	Env        map[string]string `json:"env"`
	WorkDir    string            `json:"working_directory"`
	Source     string            `json:"workflow_path"`
	Line       int               `json:"workflow_line"`
	CapturedAt time.Time         `json:"captured_at"`
	Fixed      bool              `json:"fixed"`
	FixNote    string            `json:"fix_note,omitempty"`
	FixedAt    time.Time         `json:"fixed_at,omitempty"`
}

func CaptureFailure(
	plan Plan, step Step, exitCode int, tail *LogTail, now time.Time,
) *Failure {
	return &Failure{
		JobID:      plan.JobID,
		JobName:    plan.JobName,
		Image:      plan.Image,
		StepIndex:  step.Index,
		StepName:   step.Name,
		Command:    step.Script,
		ExitCode:   exitCode,
		LogTail:    tail.Lines(),
		LogDropped: tail.Dropped(),
		Env:        maps.Clone(step.Env),
		WorkDir:    step.WorkingDir,
		Source:     plan.Source,
		Line:       step.SourceLine,
		CapturedAt: now,
	}
}

func (f Failure) Redact() Failure {
	out := f
	secrets := SecretValues(f.Env)

	out.Env = make(map[string]string, len(f.Env))
	for k, v := range f.Env {
		if IsSecretName(k) && v != "" {
			out.Env[k] = Redacted
			continue
		}
		out.Env[k] = v
	}

	out.LogTail = make([]string, len(f.LogTail))
	for i, line := range f.LogTail {
		out.LogTail[i] = replaceSecrets(line, secrets)
	}
	out.Command = replaceSecrets(f.Command, secrets)
	out.StepName = replaceSecrets(f.StepName, secrets)
	return out
}

func SecretValues(env map[string]string) []string {
	secrets := make([]string, 0, len(env))
	for k, v := range env {
		if IsSecretName(k) && v != "" {
			secrets = append(secrets, v)
		}
	}
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	return secrets
}

func RedactText(s string, env map[string]string) string {
	return replaceSecrets(s, SecretValues(env))
}

func RedactLines(lines []string, env map[string]string) []string {
	if len(lines) == 0 {
		return nil
	}
	secrets := SecretValues(env)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = replaceSecrets(line, secrets)
	}
	return out
}

func replaceSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, Redacted)
	}
	return s
}

var secretNameFragments = []string{
	"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
	"PRIVATE_KEY", "APIKEY", "API_KEY", "ACCESS_KEY", "SESSION", "AUTH",
}

func IsSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, fragment := range secretNameFragments {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

type LogTail struct {
	lines []string
	next  int
	full  bool
	total int
}

func NewLogTail(capacity int) *LogTail {
	if capacity <= 0 {
		capacity = DefaultLogTailLines
	}
	return &LogTail{lines: make([]string, capacity)}
}

func (t *LogTail) Add(line string) {
	t.lines[t.next] = line
	t.next = (t.next + 1) % len(t.lines)
	if t.next == 0 {
		t.full = true
	}
	t.total++
}

func (t *LogTail) Lines() []string {
	if !t.full {
		return slices.Clone(t.lines[:t.next])
	}
	out := make([]string, 0, len(t.lines))
	out = append(out, t.lines[t.next:]...)
	return append(out, t.lines[:t.next]...)
}

func (t *LogTail) Dropped() int {
	if !t.full {
		return 0
	}
	return t.total - len(t.lines)
}

func (t *LogTail) Reset() {
	t.next, t.full, t.total = 0, false, 0
}
