package cli

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/ui"
)

func stepClock(step time.Duration) run.Clock {
	now := time.Unix(0, 0)
	return run.ClockFunc(func() time.Time {
		now = now.Add(step)
		return now
	})
}

func fixedSize(width, height int) termSize {
	return func() (int, int) { return width, height }
}

func newTestPrinter(out *bytes.Buffer, size termSize) *pullPrinter {
	return newPullPrinter(out, ui.New(false), stepClock(time.Second), size)
}

func paintedLines(out string) []string {
	var lines []string
	for _, chunk := range strings.Split(out, "\x1b[2K")[1:] {
		line, _, _ := strings.Cut(chunk, "\n")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func pullOf(image string, layers ...run.Layer) run.PullStatus {
	return run.PullStatus{Image: image, Layers: layers}
}

func downloading(id string, current, total int64) run.Layer {
	return run.Layer{ID: id, Phase: run.LayerDownloading, Current: current, Total: total, Size: total}
}

func TestTerminalSizeIsNilOffATerminal(t *testing.T) {
	t.Parallel()

	if got := terminalSize(&bytes.Buffer{}); got != nil {
		t.Error("a non-file writer reported a terminal size")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if got := terminalSize(f); got != nil {
		t.Errorf("%s reported a terminal size; it is a character device but not a terminal", os.DevNull)
	}
	if isTerminal(f) {
		t.Errorf("%s was classified as a terminal", os.DevNull)
	}
}

func TestPullPrinterDegradesToMilestonesOffATerminal(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, nil)
	for i := range 500 {
		p.Pull(pullOf("alpine:3", downloading("a", int64(i), 500)))
	}
	p.PullDone()

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(got) > 6 {
		t.Errorf("500 updates produced %d lines, want at most 6:\n%s", len(got), out.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("output off a terminal carries escape sequences:\n%q", out.String())
	}
	if !strings.HasPrefix(got[0], "⤓ pulling alpine:3") {
		t.Errorf("first line = %q, want the pull header", got[0])
	}
	if !strings.HasPrefix(got[len(got)-1], "✓ pulled alpine:3") {
		t.Errorf("last line = %q, want the completion line", got[len(got)-1])
	}
}

func TestPullPrinterReportsEachMilestoneOnce(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, nil)
	for i := range 101 {
		p.Pull(pullOf("alpine:3", downloading("a", int64(i)*2, 200)))
	}
	for i := range 101 {
		p.Pull(pullOf("alpine:3",
			run.Layer{ID: "a", Phase: run.LayerExtracting, Current: int64(i), Total: 100, Size: 200}))
	}

	for _, want := range []string{"25%", "50%", "75%"} {
		if n := strings.Count(out.String(), want); n != 1 {
			t.Errorf("milestone %s appeared %d time(s), want 1:\n%s", want, n, out.String())
		}
	}
	if strings.Contains(out.String(), "100%") {
		t.Errorf("a 100%% milestone was printed; the completion line covers it:\n%s", out.String())
	}
}

func TestPullPrinterDoesNotRepeatALineWhenProgressJumps(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, nil)
	p.Pull(pullOf("alpine:3",
		run.Layer{ID: "a", Phase: run.LayerExtracting, Current: 60, Total: 100, Size: 100}))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")[1:]
	want := []string{
		"pulling  25%  0/1 layers",
		"pulling  50%  0/1 layers",
		"pulling  75%  0/1 layers",
	}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("one update crossing three watermarks (-want +got):\n%s", diff)
	}
}

func TestPullPrinterThrottlesTheTerminalRedraw(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newPullPrinter(&out, ui.New(false), stepClock(time.Millisecond), fixedSize(80, 40))
	for i := range 100 {
		p.Pull(pullOf("alpine:3", downloading("a", int64(i), 100)))
	}

	painted := strings.Count(out.String(), "\x1b[2K")
	if painted > 5 {
		t.Errorf("100 updates at 1ms apart repainted %d time(s); the frame interval is %v",
			painted, pullFrameInterval)
	}
	if painted == 0 {
		t.Error("nothing was painted at all")
	}
}

func TestPullPrinterLeavesOnlyTheCompletionLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, fixedSize(80, 40))
	p.Pull(pullOf("alpine:3", downloading("a", 50, 100), downloading("b", 25, 100)))
	p.PullDone()

	if !strings.Contains(out.String(), "✓ pulled alpine:3") {
		t.Errorf("no completion line in:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "200 B in") {
		t.Errorf("the completion line is missing the pulled size:\n%q", out.String())
	}
}

func TestPullPrinterCloseDoesNotClaimSuccess(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, fixedSize(80, 40))
	p.Pull(pullOf("alpine:3", downloading("a", 50, 100)))
	p.close()

	if strings.Contains(out.String(), "✓ pulled") {
		t.Errorf("an interrupted pull reported success:\n%q", out.String())
	}
}

func TestPullPrinterCloseAfterDoneIsANoOp(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, fixedSize(80, 40))
	p.Pull(pullOf("alpine:3", downloading("a", 50, 100)))
	p.PullDone()

	before := out.Len()
	p.close()
	if out.Len() != before {
		t.Errorf("close() after PullDone() wrote %q", out.String()[before:])
	}
}

func TestPullPrinterPrintsTheHeaderOnce(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, nil)
	for range 10 {
		p.Pull(pullOf("alpine:3", downloading("a", 1, 100)))
	}

	if n := strings.Count(out.String(), "⤓ pulling"); n != 1 {
		t.Errorf("the header was printed %d time(s), want 1:\n%s", n, out.String())
	}
}

func TestPullPrinterNeverPaintsWiderThanTheTerminal(t *testing.T) {
	t.Parallel()

	for _, width := range []int{20, 34, 40, 80} {
		t.Run(fmt.Sprintf("%d columns", width), func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			p := newTestPrinter(&out, fixedSize(width, 40))
			p.Pull(pullOf("a-registry.example.com/an/extremely/long/image/name:with-a-long-tag",
				downloading("aaaaaaaaaaaa", 62_000_000, 210_000_000),
				run.Layer{ID: "bbbbbbbbbbbb", Phase: run.LayerExtracting, Current: 1, Total: 2}))
			p.PullDone()

			for _, line := range paintedLines(out.String()) {
				if n := utf8.RuneCountInString(line); n > width {
					t.Errorf("painted %d columns into a %d-column terminal: %q", n, width, line)
				}
			}
			header, _, _ := strings.Cut(out.String(), "\n")
			if n := utf8.RuneCountInString(header); n > width {
				t.Errorf("the header is %d columns wide in a %d-column terminal: %q", n, width, header)
			}
		})
	}
}

func TestPullPrinterNeverPaintsTallerThanTheTerminal(t *testing.T) {
	t.Parallel()

	layers := make([]run.Layer, 0, 30)
	for i := range 30 {
		layers = append(layers, downloading(fmt.Sprintf("layer-%02d", i), int64(i), 100))
	}

	for _, height := range []int{6, 10, 24} {
		t.Run(fmt.Sprintf("%d rows", height), func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			p := newTestPrinter(&out, fixedSize(80, height))
			p.Pull(pullOf("alpine:3", layers...))

			if got := p.painted; got > height-pullReservedRows {
				t.Errorf("painted %d rows into a %d-row terminal; the block would scroll", got, height)
			}
			if p.painted == 0 {
				t.Error("nothing was painted at all")
			}
		})
	}
}

func TestPullPrinterFallsBackWhenTheSizeIsUnknown(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newTestPrinter(&out, fixedSize(0, 0))
	p.Pull(pullOf("alpine:3", downloading("aaaaaaaaaaaa", 62_000_000, 210_000_000)))

	lines := paintedLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("painted %d row(s), want 1", len(lines))
	}
	if !strings.Contains(lines[0], "62 MB/210 MB") {
		t.Errorf("row was truncated despite an unknown width: %q", lines[0])
	}
}
