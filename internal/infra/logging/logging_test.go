package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/errs"
)

func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRotatingAppendsAcrossOpens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := range 3 {
		r, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := r.Write([]byte("line\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close (run %d): %v", i, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, CurrentName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := strings.Count(string(data), "line\n"); got != 3 {
		t.Errorf("log holds %d line(s), want 3: every run must append, not truncate", got)
	}
}

func TestRotatingRollsOverAtTheSizeLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.maxSize = 64

	for range 40 {
		if _, err := r.Write(bytes.Repeat([]byte("x"), 32)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	names := segmentNames(t, dir)
	want := []string{"abel.1.jsonl", "abel.2.jsonl", "abel.3.jsonl", "abel.4.jsonl", "abel.5.jsonl", CurrentName}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestRotatingKeepsTheNewestSegmentFirst(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.maxSize = 16

	for _, marker := range []string{"first\n", "second\n", "third\n"} {
		if _, err := r.Write([]byte(marker + strings.Repeat("p", 16))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	newest, err := os.ReadFile(filepath.Join(dir, "abel.1.jsonl"))
	if err != nil {
		t.Fatalf("read abel.1.jsonl: %v", err)
	}
	if !strings.Contains(string(newest), "second") {
		t.Errorf("abel.1.jsonl = %q, want the most recently rotated segment", newest)
	}
}

func TestRotatingNeverBreaksTheRunWhenTheSinkIsGone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	n, err := r.Write([]byte("after close\n"))
	if err != nil {
		t.Errorf("Write after Close returned %v; logging must never fail a run", err)
	}
	if n != len("after close\n") {
		t.Errorf("Write reported %d bytes, want %d", n, len("after close\n"))
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}

func TestOpenReportsAnUnusableDirectory(t *testing.T) {
	t.Parallel()

	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Open(filepath.Join(blocked, "logs"))
	if err == nil {
		t.Fatal("Open succeeded under a regular file")
	}
	if got := errs.KindOf(err); got != errs.KindDependency {
		t.Errorf("kind = %q, want %q", got, errs.KindDependency)
	}
}

func TestNewFansOutToBothSinksAtTheirOwnLevels(t *testing.T) {
	t.Parallel()

	var stderr, file bytes.Buffer
	log := New(&stderr, slog.LevelWarn, &file)

	log.Debug("debug_event")
	log.Warn("warn_event")

	if strings.Contains(stderr.String(), "debug_event") {
		t.Errorf("stderr received a debug record at warn level:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "warn_event") {
		t.Errorf("stderr missed the warn record:\n%s", stderr.String())
	}
	for _, want := range []string{"debug_event", "warn_event"} {
		if !strings.Contains(file.String(), want) {
			t.Errorf("the file missed %q; it must always record everything:\n%s", want, file.String())
		}
	}
}

func TestNewEmitsOneJSONObjectPerLine(t *testing.T) {
	t.Parallel()

	var file bytes.Buffer
	New(nil, slog.LevelError, &file).Info("run_start", "job", "lint", "steps", 3)

	line := strings.TrimSpace(file.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log line is not JSON (%v): %q", err, line)
	}
	if record["msg"] != "run_start" || record["job"] != "lint" {
		t.Errorf("record = %v, want msg=run_start job=lint", record)
	}
}

func TestNewWithoutSinksIsHarmless(t *testing.T) {
	t.Parallel()

	New(nil, slog.LevelDebug, nil).Info("nothing listens")
}

func TestRunIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 50 {
		id := RunID()
		if id == "unknown" {
			t.Fatal("RunID fell back to the unknown sentinel")
		}
		if seen[id] {
			t.Errorf("RunID repeated %q; concurrent runs share one log file", id)
		}
		seen[id] = true
	}
}
