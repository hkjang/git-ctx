package logging

import (
	"log/slog"
	"testing"
)

func TestApplyAndReset(t *testing.T) {
	t.Cleanup(Reset)
	for input, expected := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	} {
		if err := Apply(input); err != nil || Level.Level() != expected {
			t.Fatalf("input=%q level=%v err=%v", input, Level.Level(), err)
		}
	}
	if err := Apply("verbose"); err == nil {
		t.Fatal("unsupported logging level was accepted")
	}
	Reset()
	if Level.Level() != slog.LevelInfo {
		t.Fatalf("reset level=%v", Level.Level())
	}
}
