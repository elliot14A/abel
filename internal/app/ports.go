// Package app holds abel's use-cases: the operations the CLI and the MCP
// server both drive. Each use-case is a struct built from its ports and
// exposing one Execute method.
//
// Nothing here knows about Docker, YAML files, kong or MCP. The ports below are
// declared by this package because this package is what needs them — the
// adapters in internal/infra satisfy them structurally.
package app

import (
	"context"

	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

// Workflows loads the workflow documents of the repository under test.
type Workflows interface {
	// Load returns every parsed workflow document, in a stable order. An empty
	// result is an error, not an empty slice: "no workflows here" is something
	// the user needs told.
	Load(ctx context.Context) ([]workflow.File, error)
}

// FailureStore persists the most recent failure per job, so that a later
// process — the MCP server serving an agent, or a second `abel` invocation —
// can read what the run that captured it saw.
type FailureStore interface {
	// Put stores the failure, replacing any previous one for the same job.
	Put(ctx context.Context, failure run.Failure) error
	// Get returns the stored failure for a job, or an error of kind
	// NOT_FOUND if the job has no recorded failure.
	Get(ctx context.Context, jobID string) (run.Failure, error)
	// Delete removes a job's failure. Deleting an absent record is not an error.
	Delete(ctx context.Context, jobID string) error
}
