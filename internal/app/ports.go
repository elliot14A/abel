package app

import (
	"context"

	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

type Workflows interface {
	Load(ctx context.Context) ([]workflow.File, error)
}

type FailureStore interface {
	Put(ctx context.Context, failure run.Failure) error
	Get(ctx context.Context, jobID string) (run.Failure, error)
	Delete(ctx context.Context, jobID string) error
}
