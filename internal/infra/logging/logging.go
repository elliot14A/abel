// Package logging builds abel's structured logger.
//
// Logs go to stderr as JSON so that stdout stays clean for the things a user
// pipes — and, more importantly, so that `abel mcp` can speak JSON-RPC on
// stdout without a log line ever corrupting the protocol stream.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Level parses a level name, defaulting to warn: abel's normal output is the
// run itself, and logs are for when something is wrong.
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

// New returns a JSON logger writing to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// Discard returns a logger that drops everything, for tests and for the paths
// where a logger is required but nothing should be emitted.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
