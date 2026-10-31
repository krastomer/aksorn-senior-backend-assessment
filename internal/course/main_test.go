package course_test

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestMain keeps request logs out of test output; tests that assert on logs install their own logger.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
