// Package runfake provides an in-memory [run.Runner] for tests.
//
// It lives beside the port it implements, not beside any one test, because the
// contract test in run/runnercontract exercises this fake and the real Docker
// adapter with the same suite — which is what keeps the fake honest.
package runfake

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

// Script tells the fake how to behave for one step, matched by the step's name.
type Script struct {
	// Output is written to the step's log writer before it exits.
	Output string
	// ExitCode is the step's exit status.
	ExitCode int
	// Err makes the step fail to execute at all, as distinct from exiting
	// non-zero — the case a real runner hits when the daemon dies mid-step.
	Err error
}

// Runner is a fake [run.Runner]. The zero value runs every step successfully
// with no output.
type Runner struct {
	// Steps maps a step name to its scripted behaviour. Unlisted steps succeed.
	Steps map[string]Script
	// StartErr makes Start fail, simulating an unavailable daemon.
	StartErr error
	// AttachErr makes Attach fail.
	AttachErr error

	mu       sync.Mutex
	sessions []*Session
}

// Start implements run.Runner.
func (r *Runner) Start(_ context.Context, plan run.Plan) (run.Session, error) {
	if r.StartErr != nil {
		return nil, r.StartErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &Session{runner: r, Plan: plan}
	r.sessions = append(r.sessions, s)
	return s, nil
}

// Sessions returns every session started so far, so a test can assert that the
// use-case closed what it opened.
func (r *Runner) Sessions() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Session(nil), r.sessions...)
}

// Session is a fake [run.Session] recording what it was asked to do.
type Session struct {
	runner *Runner
	// Plan is the plan this session was started for.
	Plan run.Plan

	mu       sync.Mutex
	executed []run.Step
	closed   int
	attached bool
}

// Exec implements run.Session.
func (s *Session) Exec(ctx context.Context, step run.Step, out io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.executed = append(s.executed, step)
	s.mu.Unlock()

	script := s.runner.Steps[step.Name]
	if script.Err != nil {
		return 0, script.Err
	}
	if script.Output != "" && out != nil {
		if _, err := io.WriteString(out, script.Output); err != nil {
			return 0, fmt.Errorf("fake runner: write step output: %w", err)
		}
	}
	return script.ExitCode, nil
}

// Attach implements run.Session.
func (s *Session) Attach(_ context.Context, _ io.Reader, stdout, _ io.Writer) error {
	if s.runner.AttachErr != nil {
		return s.runner.AttachErr
	}
	s.mu.Lock()
	s.attached = true
	s.mu.Unlock()
	if stdout != nil {
		_, _ = io.WriteString(stdout, "fake shell\n")
	}
	return nil
}

// Close implements run.Session.
func (s *Session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

// Executed returns the names of the steps that were executed, in order.
func (s *Session) Executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.executed))
	for _, step := range s.executed {
		names = append(names, step.Name)
	}
	return names
}

// Closed reports how many times the session was closed. Anything but 1 after a
// completed run is a leak or a double-free.
func (s *Session) Closed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Attached reports whether a shell was attached.
func (s *Session) Attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached
}

// Failing is a convenience for the common test shape: every step succeeds
// except one, which exits non-zero after printing output.
func Failing(step string, exitCode int, output string) *Runner {
	return &Runner{Steps: map[string]Script{
		step: {ExitCode: exitCode, Output: output},
	}}
}

// Unavailable returns a Runner whose Start fails the way a stopped Docker
// daemon does.
func Unavailable(reason string) *Runner {
	return &Runner{StartErr: errs.New(errs.KindDependency, "runfake.Start",
		"%s", strings.TrimSpace(reason))}
}
