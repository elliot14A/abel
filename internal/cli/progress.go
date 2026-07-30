package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/ui"
)

const (
	pullFrameInterval = 66 * time.Millisecond
	pullMaxRows       = 12
	pullMilestoneStep = 25
	pullReservedRows  = 2
)

type termSize func() (width, height int)

type pullPrinter struct {
	out   io.Writer
	ui    *ui.Renderer
	clock run.Clock
	size  termSize

	active    bool
	started   time.Time
	painted   int
	lastFrame time.Time
	milestone int
	last      run.PullStatus
}

func newPullPrinter(out io.Writer, r *ui.Renderer, clock run.Clock, size termSize) *pullPrinter {
	return &pullPrinter{out: out, ui: r, clock: clock, size: size}
}

func (p *pullPrinter) live() bool { return p.size != nil }

func (p *pullPrinter) Pull(status run.PullStatus) {
	p.begin(status.Image)
	p.last = status

	if !p.live() {
		p.milestones(status)
		return
	}

	now := p.clock.Now()
	if p.painted > 0 && now.Sub(p.lastFrame) < pullFrameInterval {
		return
	}
	p.lastFrame = now

	width, rows := p.bounds()
	if rows < 1 {
		p.paint(nil)
		return
	}
	p.paint(p.ui.PullLayers(status, rows, width))
}

func (p *pullPrinter) PullDone() {
	if !p.active {
		return
	}
	width, _ := p.bounds()
	_, total := p.last.Bytes()
	done := p.ui.PullDone(p.last.Image, total, p.clock.Now().Sub(p.started), width)

	if p.live() {
		p.painted++
		p.paint([]string{done})
	} else {
		fmt.Fprintln(p.out, done)
	}
	p.reset()
}

func (p *pullPrinter) close() {
	if !p.active {
		return
	}
	if p.live() {
		p.paint(nil)
	}
	p.reset()
}

func (p *pullPrinter) bounds() (width, rows int) {
	if p.size == nil {
		return 0, pullMaxRows
	}
	width, height := p.size()
	rows = pullMaxRows
	if height > 0 {
		rows = min(rows, height-pullReservedRows)
	}
	return width, max(rows, 0)
}

func (p *pullPrinter) begin(image string) {
	if p.active {
		return
	}
	p.active = true
	p.started = p.clock.Now()
	p.milestone = 0
	p.painted = 0

	width, _ := p.bounds()
	fmt.Fprintln(p.out, p.ui.PullHeader(image, width))
}

func (p *pullPrinter) reset() {
	p.active = false
	p.painted = 0
	p.milestone = 0
	p.last = run.PullStatus{}
}

func (p *pullPrinter) milestones(status run.PullStatus) {
	percent := status.Percent()
	for p.milestone+pullMilestoneStep < 100 && percent >= p.milestone+pullMilestoneStep {
		p.milestone += pullMilestoneStep
		fmt.Fprintln(p.out, p.ui.PullMilestone(p.milestone, status))
	}
}

func (p *pullPrinter) paint(lines []string) {
	var b strings.Builder
	p.moveUp(&b, p.painted)
	for _, line := range lines {
		fmt.Fprintf(&b, "\x1b[2K%s\n", line)
	}
	for i := len(lines); i < p.painted; i++ {
		b.WriteString("\x1b[2K\n")
	}
	p.moveUp(&b, p.painted-len(lines))

	fmt.Fprint(p.out, b.String())
	p.painted = len(lines)
}

func (p *pullPrinter) moveUp(b *strings.Builder, n int) {
	if n > 0 {
		fmt.Fprintf(b, "\x1b[%dA", n)
	}
}
