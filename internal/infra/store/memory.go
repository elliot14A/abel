// Package store persists captured failures.
//
// It ships two adapters for the same port: [Memory], used by tests and by
// `abel mcp --ephemeral`, and [File], the default. Both are exercised by the
// same contract suite (store_contract_test.go), which is what stops the fast
// in-memory one from quietly diverging from the real one.
package store

import (
	"context"
	"maps"
	"sync"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opMemory = "store.Memory"

// Memory is an in-process FailureStore. It is safe for concurrent use.
type Memory struct {
	mu       sync.RWMutex
	failures map[string]run.Failure
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{failures: map[string]run.Failure{}}
}

// Put stores the failure, replacing any previous one for the same job.
func (s *Memory) Put(_ context.Context, failure run.Failure) error {
	if failure.JobID == "" {
		return errs.New(errs.KindValidation, opMemory, "cannot store a failure with no job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clone the maps and slices the caller still holds, so a stored record is
	// a snapshot rather than a live view of the runner's buffers.
	failure.Env = maps.Clone(failure.Env)
	failure.LogTail = append([]string(nil), failure.LogTail...)
	s.failures[failure.JobID] = failure
	return nil
}

// Get returns the stored failure for a job.
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

// Delete removes a job's failure. Deleting an absent record is not an error.
func (s *Memory) Delete(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, jobID)
	return nil
}

func notFound(op, jobID string) error {
	return errs.New(errs.KindNotFound, op,
		"no failure recorded for job %q — run it first, or it passed", jobID).With("job", jobID)
}
