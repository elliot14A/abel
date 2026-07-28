package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opFile = "store.File"

// DefaultDir is where abel keeps state inside the repository under test.
// It belongs in .gitignore; abel writes a .gitignore into it on first use so
// that a captured failure never lands in a commit.
const DefaultDir = ".abel"

// File persists failures as one JSON document per job under a directory.
//
// The on-disk form is what makes the MCP server useful across processes: `abel
// run lint` in one terminal and an agent's `get_failure` in another are
// different processes, and this is how they share.
type File struct {
	dir string
}

// NewFile returns a store rooted at dir. The directory is created lazily, on
// the first write, so that a read-only command never leaves a stray directory
// in the user's repository.
func NewFile(dir string) *File {
	return &File{dir: dir}
}

// Dir returns the store's root directory.
func (s *File) Dir() string { return s.dir }

// Put writes the failure atomically: a temporary file plus a rename, so a
// reader never observes a half-written record.
func (s *File) Put(_ context.Context, failure run.Failure) error {
	if failure.JobID == "" {
		return errs.New(errs.KindValidation, opFile, "cannot store a failure with no job ID")
	}
	path, err := s.pathFor(failure.JobID)
	if err != nil {
		return err
	}
	if err := s.ensureDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(failure, "", "  ")
	if err != nil {
		return errs.New(errs.KindInternal, opFile,
			"cannot encode the failure for job %q", failure.JobID).Wrapping(err)
	}

	tmp, err := os.CreateTemp(s.dir, ".failure-*.json.tmp")
	if err != nil {
		return errs.New(errs.KindDependency, opFile,
			"cannot write to %s", s.dir).Wrapping(err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort: after a successful rename this fails, which is fine.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.New(errs.KindDependency, opFile, "cannot write %s", tmpName).Wrapping(err)
	}
	if err := tmp.Close(); err != nil {
		return errs.New(errs.KindDependency, opFile, "cannot close %s", tmpName).Wrapping(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errs.New(errs.KindDependency, opFile,
			"cannot publish the failure record for job %q", failure.JobID).Wrapping(err)
	}
	return nil
}

// Get reads a job's stored failure.
func (s *File) Get(_ context.Context, jobID string) (run.Failure, error) {
	path, err := s.pathFor(jobID)
	if err != nil {
		return run.Failure{}, err
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is derived from a validated job ID
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return run.Failure{}, notFound(opFile, jobID)
	case err != nil:
		return run.Failure{}, errs.New(errs.KindDependency, opFile,
			"cannot read the failure record for job %q", jobID).With("path", path).Wrapping(err)
	}

	var failure run.Failure
	if err := json.Unmarshal(data, &failure); err != nil {
		// A corrupt record is not worth failing a run over, but silently
		// ignoring it would be worse: say what to delete.
		return run.Failure{}, errs.New(errs.KindValidation, opFile,
			"the failure record for job %q is corrupt; delete %s and re-run", jobID, path).
			With("path", path).Wrapping(err)
	}
	return failure, nil
}

// Delete removes a job's failure. Deleting an absent record is not an error.
func (s *File) Delete(_ context.Context, jobID string) error {
	path, err := s.pathFor(jobID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errs.New(errs.KindDependency, opFile,
			"cannot delete the failure record for job %q", jobID).With("path", path).Wrapping(err)
	}
	return nil
}

func (s *File) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return errs.New(errs.KindDependency, opFile,
			"cannot create the state directory %s", s.dir).Wrapping(err)
	}
	// abel's state is per-checkout scratch, never something to commit.
	gitignore := filepath.Join(s.dir, ".gitignore")
	if _, err := os.Stat(gitignore); errors.Is(err, fs.ErrNotExist) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o600)
	}
	return nil
}

// pathFor maps a job ID to a file name, rejecting anything that could escape
// the store directory. Job IDs come from a YAML file abel did not write, so
// this is a real boundary, not a formality.
func (s *File) pathFor(jobID string) (string, error) {
	if err := validJobID(jobID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, jobID+".json"), nil
}

func validJobID(jobID string) error {
	switch {
	case jobID == "":
		return errs.New(errs.KindValidation, opFile, "a job name is required")
	case strings.ContainsAny(jobID, `/\`) || strings.Contains(jobID, ".."):
		return errs.New(errs.KindValidation, opFile,
			"job name %q contains a path separator", jobID).With("job", jobID)
	case jobID != filepath.Base(jobID):
		return errs.New(errs.KindValidation, opFile,
			"job name %q is not a valid file name", jobID).With("job", jobID)
	default:
		return nil
	}
}
