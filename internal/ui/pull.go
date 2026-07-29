package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/elliot14A/abel/internal/core/run"
)

const (
	barWidth  = 10
	barFull   = "▓"
	barEmpty  = "░"
	idWidth   = 12
	minRows   = 2
	ellipsis  = "…"
	unitStep  = 1000
	oneDigit  = 10
	byteUnits = " kMG"
)

type seg struct {
	text   string
	style  lipgloss.Style
	styled bool
}

func plain(text string) seg { return seg{text: text} }

func tinted(style lipgloss.Style, text string) seg {
	return seg{text: text, style: style, styled: true}
}

func (r *Renderer) join(maxWidth int, segs ...seg) string {
	var b strings.Builder
	remaining := maxWidth

	for _, s := range segs {
		if maxWidth > 0 {
			if remaining <= 0 {
				break
			}
			n := utf8.RuneCountInString(s.text)
			if n > remaining {
				s.text = truncate(s.text, remaining)
				b.WriteString(r.render(s))
				break
			}
			remaining -= n
		}
		b.WriteString(r.render(s))
	}
	return b.String()
}

func (r *Renderer) render(s seg) string {
	if !s.styled {
		return s.text
	}
	return r.style(s.style, s.text)
}

func truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth-1]) + ellipsis
}

func (r *Renderer) PullHeader(image string, maxWidth int) string {
	return r.join(maxWidth,
		tinted(styleCyan, "⤓"),
		plain(" "),
		tinted(styleDim, "pulling"),
		plain(" "),
		plain(image))
}

func (r *Renderer) PullLayers(status run.PullStatus, maxRows, maxWidth int) []string {
	shown, hidden := visibleLayers(status.Layers, maxRows)

	lines := make([]string, 0, len(shown)+1)
	for _, layer := range shown {
		lines = append(lines, r.pullLayer(layer, maxWidth))
	}
	if hidden > 0 {
		lines = append(lines, r.join(maxWidth,
			plain("  "),
			tinted(styleDim, fmt.Sprintf("%s +%d more layer(s)", ellipsis, hidden))))
	}
	return lines
}

func (r *Renderer) pullLayer(layer run.Layer, maxWidth int) string {
	style := styleCyan
	if layer.Phase.Done() {
		style = styleGreen
	}
	return r.join(maxWidth,
		plain("  "),
		tinted(styleDim, fmt.Sprintf("%-*s", idWidth, layer.ID)),
		plain("  "),
		tinted(style, bar(layer.Fraction())),
		plain("  "),
		tinted(styleDim, layerLabel(layer)))
}

func (r *Renderer) PullMilestone(percent int, status run.PullStatus) string {
	return fmt.Sprintf("pulling  %d%%  %d/%d layers",
		percent, status.Complete(), len(status.Layers))
}

func (r *Renderer) PullDone(image string, bytes int64, elapsed time.Duration, maxWidth int) string {
	tail := duration(elapsed)
	if bytes > 0 {
		tail = humanBytes(bytes) + " in " + tail
	}
	return r.join(maxWidth,
		tinted(styleGreen, "✓"),
		plain(" "),
		tinted(styleDim, "pulled"),
		plain(" "),
		plain(image),
		plain(" "),
		tinted(styleDim, tail))
}

func visibleLayers(layers []run.Layer, maxRows int) ([]run.Layer, int) {
	if maxRows < minRows {
		maxRows = minRows
	}
	if len(layers) <= maxRows {
		return layers, 0
	}

	live := 0
	for _, layer := range layers {
		if !layer.Phase.Done() {
			live++
		}
	}
	budget := maxRows - 1
	live = min(live, budget)
	done := budget - live

	shown := make([]run.Layer, 0, budget)
	for _, layer := range layers {
		switch {
		case !layer.Phase.Done() && live > 0:
			shown, live = append(shown, layer), live-1
		case layer.Phase.Done() && done > 0:
			shown, done = append(shown, layer), done-1
		}
	}
	return shown, len(layers) - len(shown)
}

func layerLabel(layer run.Layer) string {
	switch layer.Phase {
	case run.LayerDownloading:
		if layer.Total > 0 {
			return humanBytes(min(layer.Current, layer.Total)) + "/" + humanBytes(layer.Total)
		}
		return "downloading"
	case run.LayerComplete:
		return "pull complete"
	case run.LayerExists:
		return "already exists"
	default:
		return string(layer.Phase)
	}
}

func bar(fraction float64) string {
	filled := min(max(int(fraction*barWidth), 0), barWidth)
	return strings.Repeat(barFull, filled) + strings.Repeat(barEmpty, barWidth-filled)
}

func humanBytes(n int64) string {
	if n < unitStep {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	unit := 0
	for value >= unitStep && unit < len(byteUnits)-1 {
		value /= unitStep
		unit++
	}
	if value < oneDigit {
		return fmt.Sprintf("%.1f %cB", value, byteUnits[unit])
	}
	return fmt.Sprintf("%.0f %cB", value, byteUnits[unit])
}
