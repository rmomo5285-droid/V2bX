package hy2

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitLoggerWritesConfiguredOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zap keeps file sinks open for the process lifetime on Windows")
	}
	path := filepath.Join(t.TempDir(), "hy2.log")
	logger, err := initLogger("warn", "json", path)
	if err != nil {
		t.Fatalf("initialize logger: %v", err)
	}
	logger.Warn("policy-audit-test")
	if err := logger.Sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read logger output: %v", err)
	}
	if !strings.Contains(string(data), "policy-audit-test") {
		t.Fatalf("logger output missing message: %q", data)
	}
}
