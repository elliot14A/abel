package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/run"
)

func TestBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fraction float64
		want     string
	}{
		{name: "empty", fraction: 0, want: "░░░░░░░░░░"},
		{name: "half", fraction: 0.5, want: "▓▓▓▓▓░░░░░"},
		{name: "full", fraction: 1, want: "▓▓▓▓▓▓▓▓▓▓"},
		{name: "clamps above one", fraction: 3, want: "▓▓▓▓▓▓▓▓▓▓"},
		{name: "clamps below zero", fraction: -1, want: "░░░░░░░░░░"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := bar(tt.fraction); got != tt.want {
				t.Errorf("bar(%v) = %q, want %q", tt.fraction, got, tt.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int64
		want string
	}{
		{in: 0, want: "0 B"},
		{in: 999, want: "999 B"},
		{in: 1000, want: "1.0 kB"},
		{in: 9_900, want: "9.9 kB"},
		{in: 62_000_000, want: "62 MB"},
		{in: 540_000_000, want: "540 MB"},
		{in: 2_500_000_000, want: "2.5 GB"},
		{in: 5_000_000_000_000, want: "5000 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := humanBytes(tt.in); got != tt.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPullLayersRendersOneRowPerLayer(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{
		{ID: "aaaaaaaaaaaa", Phase: run.LayerComplete},
		{ID: "bbbbbbbbbbbb", Phase: run.LayerExtracting, Current: 50, Total: 100},
		{ID: "cccccccccccc", Phase: run.LayerDownloading, Current: 62_000_000, Total: 210_000_000},
		{ID: "dddddddddddd", Phase: run.LayerWaiting},
	}}

	want := []string{
		"  aaaaaaaaaaaa  ▓▓▓▓▓▓▓▓▓▓  pull complete",
		"  bbbbbbbbbbbb  ▓▓▓▓▓▓▓░░░  extracting",
		"  cccccccccccc  ▓░░░░░░░░░  62 MB/210 MB",
		"  dddddddddddd  ░░░░░░░░░░  waiting",
	}
	if diff := cmp.Diff(want, New(false).PullLayers(status, 12, 0)); diff != "" {
		t.Errorf("rows mismatch (-want +got):\n%s", diff)
	}
}

func TestPullLayersKeepsTheLayersStillInFlight(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{
		{ID: "done-1", Phase: run.LayerComplete},
		{ID: "done-2", Phase: run.LayerComplete},
		{ID: "busy-1", Phase: run.LayerDownloading, Current: 1, Total: 2},
		{ID: "done-3", Phase: run.LayerComplete},
		{ID: "busy-2", Phase: run.LayerExtracting},
	}}

	got := New(false).PullLayers(status, 3, 0)
	if len(got) != 3 {
		t.Fatalf("rendered %d row(s), want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, want := range []string{"busy-1", "busy-2"} {
		if !strings.Contains(got[0]+got[1], want) {
			t.Errorf("row for %q was dropped in favour of a finished layer:\n%s",
				want, strings.Join(got, "\n"))
		}
	}
	if !strings.Contains(got[2], "+3 more layer(s)") {
		t.Errorf("last row = %q, want the hidden-layer count", got[2])
	}
}

func TestPullLayersKeepsTheWorkflowFileOrder(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{
		{ID: "first", Phase: run.LayerComplete},
		{ID: "second", Phase: run.LayerDownloading},
		{ID: "third", Phase: run.LayerComplete},
	}}

	got := New(false).PullLayers(status, 3, 0)
	for i, want := range []string{"first", "second", "third"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("row %d = %q, want the layer %q (first-seen order must hold)", i, got[i], want)
		}
	}
}

func TestPullSummaryLines(t *testing.T) {
	t.Parallel()

	r := New(false)
	status := run.PullStatus{Layers: []run.Layer{
		{ID: "a", Phase: run.LayerComplete, Size: 100},
		{ID: "b", Phase: run.LayerWaiting},
	}}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "header", got: r.PullHeader("alpine:3", 0), want: "⤓ pulling alpine:3"},
		{name: "milestone", got: r.PullMilestone(50, status), want: "pulling  50%  1/2 layers"},
		{
			name: "done",
			got:  r.PullDone("alpine:3", 540_000_000, 41200*time.Millisecond, 0),
			want: "✓ pulled alpine:3 540 MB in 41.2s",
		},
		{
			name: "done with every layer already cached",
			got:  r.PullDone("alpine:3", 0, 300*time.Millisecond, 0),
			want: "✓ pulled alpine:3 300ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("= %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestPullRenderingIsColourGated(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{{ID: "a", Phase: run.LayerComplete}}}
	for _, line := range New(false).PullLayers(status, 12, 0) {
		if strings.Contains(line, "\x1b") {
			t.Errorf("colour was disabled but %q carries an escape sequence", line)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       string
		maxWidth int
		want     string
	}{
		{name: "leaves a short string alone", in: "abc", maxWidth: 10, want: "abc"},
		{name: "leaves an exact fit alone", in: "abc", maxWidth: 3, want: "abc"},
		{name: "marks a cut with an ellipsis", in: "abcdef", maxWidth: 4, want: "abc…"},
		{name: "collapses to the ellipsis alone", in: "abcdef", maxWidth: 1, want: "…"},
		{name: "vanishes at zero", in: "abcdef", maxWidth: 0, want: ""},
		{name: "counts runes, not bytes", in: "▓▓▓▓▓░░░░░", maxWidth: 4, want: "▓▓▓…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := truncate(tt.in, tt.maxWidth); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestPullLayersRespectsAWidthBudget(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{
		{ID: "4a633c382b06", Phase: run.LayerDownloading, Current: 62_000_000, Total: 210_000_000},
	}}

	tests := []struct {
		width int
		want  string
	}{
		{width: 0, want: "  4a633c382b06  ▓░░░░░░░░░  62 MB/210 MB"},
		{width: 80, want: "  4a633c382b06  ▓░░░░░░░░░  62 MB/210 MB"},
		{width: 34, want: "  4a633c382b06  ▓░░░░░░░░░  62 MB…"},
		{width: 20, want: "  4a633c382b06  ▓░░…"},
		{width: 10, want: "  4a633c3…"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d columns", tt.width), func(t *testing.T) {
			t.Parallel()
			got := New(false).PullLayers(status, 12, tt.width)[0]
			if got != tt.want {
				t.Errorf("at %d columns =\n  %q\nwant\n  %q", tt.width, got, tt.want)
			}
		})
	}
}

func TestTruncationNeverSplitsAnEscapeSequence(t *testing.T) {
	t.Parallel()

	status := run.PullStatus{Layers: []run.Layer{
		{ID: "4a633c382b06", Phase: run.LayerDownloading, Current: 1, Total: 2},
	}}

	for width := 1; width <= 60; width++ {
		line := New(true).PullLayers(status, 12, width)[0]
		if strings.Count(line, "\x1b[") != strings.Count(line, "m") {
			t.Errorf("at %d columns the styling is unbalanced: %q", width, line)
		}
		if strings.HasSuffix(line, "\x1b") || strings.HasSuffix(line, "\x1b[") {
			t.Errorf("at %d columns the line ends mid-escape: %q", width, line)
		}
	}
}

func TestPullHeaderAndDoneRespectAWidthBudget(t *testing.T) {
	t.Parallel()

	r := New(false)
	image := "a-registry.example.com/an/extremely/long/image/name:with-a-long-tag"

	for width := 1; width <= 40; width++ {
		header := r.PullHeader(image, width)
		if n := utf8.RuneCountInString(header); n > width {
			t.Errorf("PullHeader at %d columns is %d wide: %q", width, n, header)
		}
		done := r.PullDone(image, 540_000_000, time.Second, width)
		if n := utf8.RuneCountInString(done); n > width {
			t.Errorf("PullDone at %d columns is %d wide: %q", width, n, done)
		}
	}
}

func TestFailureShowsAnUnverifiedClaim(t *testing.T) {
	t.Parallel()

	f := run.Failure{
		JobID: "lint", StepName: "typecheck", ExitCode: 2,
		Fixed:   true,
		FixNote: "widened the tsconfig target",
		FixedAt: time.Date(2026, 8, 2, 14, 31, 7, 0, time.UTC),
	}

	got := New(false).Failure(f)
	for _, want := range []string{
		"claimed", "fixed but not verified", "2026-08-02T14:31:07Z", "widened the tsconfig target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure block is missing %q:\n%s", want, got)
		}
	}

	if strings.Contains(New(false).Failure(run.Failure{JobID: "lint"}), "claimed") {
		t.Error("an unclaimed failure rendered a claim line")
	}
}

func TestFailureAnnouncesDroppedLogLines(t *testing.T) {
	t.Parallel()

	r := New(false)
	base := run.Failure{JobID: "lint", LogTail: []string{"four", "five"}}

	if got := r.Failure(base); !strings.Contains(got, "last 2 line(s):") {
		t.Errorf("an untruncated tail should not mention drops:\n%s", got)
	}

	base.LogDropped = 3
	got := r.Failure(base)
	if !strings.Contains(got, "last 2 line(s), 3 earlier line(s) dropped:") {
		t.Errorf("a truncated tail must say so:\n%s", got)
	}
}
