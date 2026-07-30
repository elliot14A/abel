package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/elliot14A/abel/internal/core/errs"
)

const opOpen = "logging.Open"

const (
	DefaultLogDir = "logs"
	CurrentName   = "abel.jsonl"
	MaxBytes      = 10 << 20
	KeepSegments  = 5
	dirPerm       = 0o750
	filePerm      = 0o640
)

type Rotating struct {
	mu      sync.Mutex
	dir     string
	file    *os.File
	size    int64
	maxSize int64
	keep    int
}

func Open(dir string) (*Rotating, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, errs.New(errs.KindDependency, opOpen,
			"cannot create the log directory %s", dir).With("dir", dir).Wrapping(err)
	}

	r := &Rotating{dir: dir, maxSize: MaxBytes, keep: KeepSegments}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) Path() string { return filepath.Join(r.dir, CurrentName) }

func (r *Rotating) open() error {
	path := r.Path()
	//nolint:gosec // path is CurrentName under the log directory abel was configured with
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return errs.New(errs.KindDependency, opOpen,
			"cannot open the log file %s", path).With("path", path).Wrapping(err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return errs.New(errs.KindDependency, opOpen,
			"cannot measure the log file %s", path).With("path", path).Wrapping(err)
	}

	r.file, r.size = f, info.Size()
	return nil
}

func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return len(p), nil
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		if err := r.rotate(); err != nil {
			return len(p), nil
		}
	}

	n, err := r.file.Write(p)
	r.size += int64(n)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

func (r *Rotating) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	r.file = nil

	oldest := r.segment(r.keep)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for i := r.keep - 1; i >= 1; i-- {
		if err := os.Rename(r.segment(i), r.segment(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(r.Path(), r.segment(1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return r.open()
}

func (r *Rotating) segment(n int) string {
	return filepath.Join(r.dir, fmt.Sprintf("abel.%d.jsonl", n))
}

func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
