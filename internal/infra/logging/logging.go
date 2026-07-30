package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
)

const runIDBytes = 4

func Level(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	case "", "warn", "warning":
		return slog.LevelWarn
	default:
		return slog.LevelWarn
	}
}

func New(stderr io.Writer, level slog.Level, file io.Writer) *slog.Logger {
	handlers := make(fanout, 0, 2)
	if stderr != nil {
		handlers = append(handlers, slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	}
	if file != nil {
		handlers = append(handlers,
			slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	if len(handlers) == 0 {
		return discard()
	}
	return slog.New(handlers)
}

func discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func RunID() string {
	var b [runIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, record slog.Record) error {
	for _, h := range f {
		if !h.Enabled(ctx, record.Level) {
			continue
		}
		if err := h.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	if name == "" {
		return f
	}
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
