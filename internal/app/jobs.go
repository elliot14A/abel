package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/workflow"
)

// JobRef locates a job: which document declares it, and under which ID.
type JobRef struct {
	JobID        string
	JobName      string
	WorkflowName string
	WorkflowPath string
	// RunsOn is shown when listing jobs, so the user can see at a glance which
	// ones abel will be able to reproduce.
	RunsOn []string
}

// ListJobs enumerates every job abel can see. It is the use-case behind
// `abel jobs` and behind the "did you mean" list on an unknown job.
type ListJobs struct {
	workflows Workflows
}

// NewListJobs builds the use-case.
func NewListJobs(workflows Workflows) *ListJobs {
	return &ListJobs{workflows: workflows}
}

// Execute returns every job in workflow-file order, then declaration order.
func (u *ListJobs) Execute(ctx context.Context) ([]JobRef, error) {
	files, err := u.workflows.Load(ctx)
	if err != nil {
		return nil, err
	}
	return jobRefs(files), nil
}

func jobRefs(files []workflow.File) []JobRef {
	var refs []JobRef
	for _, f := range files {
		for _, id := range f.JobIDs {
			job, ok := f.Job(id)
			if !ok {
				continue
			}
			refs = append(refs, JobRef{
				JobID:        id,
				JobName:      job.Name,
				WorkflowName: f.Name,
				WorkflowPath: f.Path,
				RunsOn:       job.RunsOn,
			})
		}
	}
	return refs
}

const opFindJob = "app.findJob"

// findJob resolves a job ID across every workflow document.
//
// Two documents declaring the same job ID is a genuine ambiguity — GitHub scopes
// job IDs per workflow, abel is asked for one by name — so it is reported as a
// conflict rather than silently resolved to the first match.
func findJob(files []workflow.File, jobID string) (workflow.File, error) {
	var matches []workflow.File
	for _, f := range files {
		if _, ok := f.Job(jobID); ok {
			matches = append(matches, f)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return workflow.File{}, errs.New(errs.KindNotFound, opFindJob,
			"no job %q in any workflow%s", jobID, availableJobs(files)).With("job", jobID)
	default:
		paths := make([]string, 0, len(matches))
		for _, f := range matches {
			paths = append(paths, f.Path)
		}
		return workflow.File{}, errs.New(errs.KindConflict, opFindJob,
			"job %q is declared in more than one workflow (%s); rename one or pass --workflow",
			jobID, strings.Join(paths, ", ")).With("job", jobID)
	}
}

// availableJobs renders the "available: …" suffix of a not-found message. An
// unknown job is almost always a typo, so the alternatives belong in the error
// itself rather than behind a second command.
func availableJobs(files []workflow.File) string {
	refs := jobRefs(files)
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.JobID)
	}
	return fmt.Sprintf(" (available: %s)", strings.Join(names, ", "))
}
