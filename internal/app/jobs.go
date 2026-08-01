package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/workflow"
)

type JobRef struct {
	JobID        string
	JobName      string
	WorkflowName string
	WorkflowPath string
	RunsOn       []string
	Runnable     bool
	Unsupported  string
}

type ListJobs struct {
	workflows Workflows
}

func NewListJobs(workflows Workflows) *ListJobs {
	return &ListJobs{workflows: workflows}
}

func (u *ListJobs) Execute(ctx context.Context) ([]JobRef, error) {
	files, err := u.workflows.Load(ctx)
	if err != nil {
		return nil, err
	}

	refs := jobRefs(files)
	byPath := make(map[string]workflow.File, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}
	for i, ref := range refs {
		file, ok := byPath[ref.WorkflowPath]
		if !ok {
			continue
		}
		if _, err := workflow.Resolve(file, ref.JobID, workflow.Options{}); err != nil {
			refs[i].Unsupported = err.Error()
			continue
		}
		refs[i].Runnable = true
	}
	return refs, nil
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
