package testutil

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
)

// NewTestLogger returns a logger that discards all output for clean test runs
func NewTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// NewCaptureLogger returns a logger that captures output to a buffer for assertions
func NewCaptureLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return logger, &buf
}
