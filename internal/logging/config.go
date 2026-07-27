package logging

import (
	"errors"
	"log/slog"
	"strings"
)

// Level is shared by the process-wide JSON handler so an administrator can
// change verbosity without restarting the service.
var Level slog.LevelVar

func init() {
	Level.Set(slog.LevelInfo)
}

func Parse(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("logging.level must be debug, info, warn, or error")
	}
}

func Apply(value string) error {
	level, err := Parse(value)
	if err != nil {
		return err
	}
	Level.Set(level)
	return nil
}

func Reset() {
	Level.Set(slog.LevelInfo)
}
