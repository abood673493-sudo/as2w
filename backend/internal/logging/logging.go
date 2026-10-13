// Package logging configures the structured logger used across the panel.
package logging

import (
	"log/slog"
	"os"
)

// New returns a slog logger. JSON output is used in production so that logs can
// be shipped to journald/ELK without a parser; a human friendly text handler is
// used in development.
func New(level string, production bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if production {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}