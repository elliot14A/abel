// Package workflow models the subset of GitHub Actions workflow syntax that
// abel reproduces locally, and resolves a job in that model into a concrete,
// runnable plan.
//
// The package is pure: it turns bytes into values and values into plans. It
// performs no file I/O, starts no containers, and reads no environment. The one
// dependency it carries is goccy/go-yaml, a pure decoder — the same carve-out
// the house standard makes for a schema library in the core ring, and it is
// what lets every parse error point at a line and column.
package workflow

// File is a parsed workflow document.
type File struct {
	// Path is the source path, retained only for diagnostics.
	Path string
	// Name is the workflow's `name:`, or the file's base name if absent.
	Name string
	// Env is the workflow-level `env:` map, the lowest-precedence layer.
	Env map[string]string
	// Defaults is the workflow-level `defaults.run:` block.
	Defaults Defaults
	// Jobs is keyed by job ID (the YAML key, not the display name).
	Jobs map[string]Job
	// JobIDs lists job IDs in declaration order. Ranging over Jobs is
	// non-deterministic; anything user-visible must range over this.
	JobIDs []string
}

// Job returns the job with the given ID and whether it exists.
func (f File) Job(id string) (Job, bool) {
	j, ok := f.Jobs[id]
	return j, ok
}

// Defaults is a `defaults.run:` block, at either workflow or job level.
type Defaults struct {
	Shell            string
	WorkingDirectory string
}

// Job is a single job in a workflow.
type Job struct {
	ID   string
	Name string
	// RunsOn holds the `runs-on:` labels, normalised to a slice whether the
	// source used a scalar or a sequence.
	RunsOn []string
	// Container is the `container:` block. A non-empty Image overrides the
	// image that RunsOn would otherwise imply.
	Container Container
	Env       map[string]string
	Defaults  Defaults
	Steps     []Step
	// Needs, If and Strategy are parsed only so that Resolve can warn about
	// them; abel evaluates none of them.
	Needs       []string
	If          string
	HasStrategy bool
	// Line is the 1-based source line of the job key.
	Line int
}

// Container is a job's `container:` block.
type Container struct {
	Image string
	Env   map[string]string
	// Options carries `container.options` verbatim; abel warns rather than
	// silently dropping it.
	Options string
}

// Step is one entry in a job's `steps:` list.
type Step struct {
	Name string
	// Uses is the action reference for `uses:` steps, empty for `run:` steps.
	Uses string
	// Run is the shell script for `run:` steps, empty for `uses:` steps.
	Run              string
	Shell            string
	WorkingDirectory string
	If               string
	Env              map[string]string
	// Line is the 1-based source line of the step.
	Line int
}

// IsRun reports whether this is a `run:` step — the kind abel executes.
func (s Step) IsRun() bool { return s.Run != "" }

// Label returns the step's display name, falling back to the action reference
// or a truncated form of the script, mirroring how GitHub labels steps.
func (s Step) Label() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Uses != "":
		return s.Uses
	default:
		return firstLine(s.Run, 60)
	}
}

func firstLine(s string, maxLen int) string {
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > maxLen {
		return s[:maxLen-1] + "…"
	}
	return s
}
