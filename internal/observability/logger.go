package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a slog.Logger configured for JSON output to stdout.
// Level is parsed from the level argument, defaulting to info on unknown values.
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}
