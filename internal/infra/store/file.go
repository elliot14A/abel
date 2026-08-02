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

const DefaultDir = ".abel"

type File struct {
	dir string
}

func NewFile(dir string) *File {
	return &File{dir: dir}
}

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
		return run.Failure{}, errs.New(errs.KindValidation, opFile,
			"the failure record for job %q is corrupt; delete %s and re-run", jobID, path).
			With("path", path).Wrapping(err)
	}
	return failure, nil
}

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

	gitignore := filepath.Join(s.dir, ".gitignore")
	if _, err := os.Stat(gitignore); errors.Is(err, fs.ErrNotExist) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o600)
	}
	return nil
}

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
