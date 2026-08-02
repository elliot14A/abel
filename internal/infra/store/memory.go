package store

import (
	"context"
	"maps"
	"sync"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opMemory = "store.Memory"

type Memory struct {
	mu       sync.RWMutex
	failures map[string]run.Failure
}

func NewMemory() *Memory {
	return &Memory{failures: map[string]run.Failure{}}
}

func (s *Memory) Put(_ context.Context, failure run.Failure) error {
	if failure.JobID == "" {
		return errs.New(errs.KindValidation, opMemory, "cannot store a failure with no job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	failure.Env = maps.Clone(failure.Env)
	failure.LogTail = append([]string(nil), failure.LogTail...)
	s.failures[failure.JobID] = failure
	return nil
}

func (s *Memory) Get(_ context.Context, jobID string) (run.Failure, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	failure, ok := s.failures[jobID]
	if !ok {
		return run.Failure{}, notFound(opMemory, jobID)
	}
	failure.Env = maps.Clone(failure.Env)
	failure.LogTail = append([]string(nil), failure.LogTail...)
	return failure, nil
}

func (s *Memory) Delete(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, jobID)
	return nil
}

func notFound(op, jobID string) error {
	return errs.New(errs.KindNotFound, op,
		"no failure recorded for job %q; run it first, or it passed", jobID).With("job", jobID)
}
