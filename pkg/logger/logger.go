// Package logger builds the process logger. log/slog only — no third-party
// logger, no fmt.Println, and never any PII in a log line (CLAUDE.md rule 13).
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Logs go to STDERR, not stdout.
//
// stdout belongs to a command's own output: the report from --role=slo or
// --role=loadtest is meant to be read and piped, and twenty interleaved INFO
// lines make it unparseable by anything including a human. Container runtimes
// capture both streams, so nothing is lost for the server role.
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
