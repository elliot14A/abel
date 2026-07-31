package runfake

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

type Script struct {
	Output   string
	ExitCode int
	Err      error
	Delay    time.Duration
}

type Runner struct {
	Steps     map[string]Script
	StartErr  error
	AttachErr error
	mu        sync.Mutex
	sessions  []*Session
}

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

func (r *Runner) Sessions() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Session(nil), r.sessions...)
}

type Session struct {
	runner   *Runner
	Plan     run.Plan
	mu       sync.Mutex
	executed []run.Step
	closed   int
	attached bool
}

func (s *Session) Exec(ctx context.Context, step run.Step, out io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.executed = append(s.executed, step)
	s.mu.Unlock()

	script := s.runner.Steps[step.Name]
	if script.Delay > 0 {
		timer := time.NewTimer(script.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
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

func (s *Session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func (s *Session) Executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.executed))
	for _, step := range s.executed {
		names = append(names, step.Name)
	}
	return names
}

func (s *Session) Closed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) Attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached
}

func Failing(step string, exitCode int, output string) *Runner {
	return &Runner{Steps: map[string]Script{
		step: {ExitCode: exitCode, Output: output},
	}}
}

func Unavailable(reason string) *Runner {
	return &Runner{StartErr: errs.New(errs.KindDependency, "runfake.Start",
		"%s", strings.TrimSpace(reason))}
}
