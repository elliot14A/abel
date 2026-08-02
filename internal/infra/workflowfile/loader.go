package workflowfile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/workflow"
)

const opLoad = "workflowfile.Load"

const DefaultDir = ".github/workflows"

const maxFileSize = 1 << 20

type Loader struct {
	root string
	dir  string
}

func NewLoader(root, dir string) *Loader {
	return &Loader{root: root, dir: dir}
}

func (l *Loader) displayPath(path string) string {
	if l.root == "" {
		return path
	}
	rel, err := filepath.Rel(l.root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func (l *Loader) Load(ctx context.Context) ([]workflow.File, error) {
	entries, err := os.ReadDir(l.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errs.New(errs.KindNotFound, opLoad,
			"no workflow directory at %s; is this the root of a repository with GitHub Actions?", l.dir).
			With("dir", l.dir)
	case err != nil:
		return nil, errs.New(errs.KindDependency, opLoad,
			"cannot read %s", l.dir).With("dir", l.dir).Wrapping(err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isWorkflowFile(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)

	files := make([]workflow.File, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, errs.New(errs.KindCancelled, opLoad, "cancelled while reading workflows").Wrapping(err)
		}
		file, err := l.read(filepath.Join(l.dir, name))
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}

	if len(files) == 0 {
		return nil, errs.New(errs.KindNotFound, opLoad,
			"no workflow files in %s", l.dir).With("dir", l.dir)
	}
	return files, nil
}

func (l *Loader) read(path string) (workflow.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return workflow.File{}, errs.New(errs.KindDependency, opLoad,
			"cannot stat %s", path).With("path", path).Wrapping(err)
	}
	if info.Size() > maxFileSize {
		return workflow.File{}, errs.New(errs.KindValidation, opLoad,
			"%s is %d bytes; abel reads workflow files up to %d", path, info.Size(), maxFileSize).
			With("path", path)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path comes from a directory listing of l.dir
	if err != nil {
		return workflow.File{}, errs.New(errs.KindDependency, opLoad,
			"cannot read %s", path).With("path", path).Wrapping(err)
	}
	return workflow.Parse(l.displayPath(path), data)
}

func isWorkflowFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}
