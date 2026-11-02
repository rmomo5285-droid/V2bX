package clashapi

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func uiZIP(t *testing.T, name string, mode os.FileMode) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	h := &zip.FileHeader{Name: name}
	h.SetMode(mode)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("ui")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestExternalUIArchiveConfinement(t *testing.T) {
	s := &Server{ctx: context.Background()}
	for _, name := range []string{"../escape", "repo/../../escape", "/escape", `repo\..\escape`, "C:/escape"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			output := filepath.Join(parent, "ui")
			if err := os.Mkdir(output, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := s.downloadZIP(bytes.NewReader(uiZIP(t, name, 0o644)), output); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
				t.Fatalf("outside file created: %v", err)
			}
		})
	}
	for _, name := range []string{"index.html", "repo/index.html", "repo/assets/index.html"} {
		t.Run("valid-"+name, func(t *testing.T) {
			output := t.TempDir()
			if err := s.downloadZIP(bytes.NewReader(uiZIP(t, name, 0o644)), output); err != nil {
				t.Fatal(err)
			}
			p := "index.html"
			if name == "repo/assets/index.html" {
				p = "assets/index.html"
			}
			data, err := os.ReadFile(filepath.Join(output, p))
			if err != nil || string(data) != "ui" {
				t.Fatalf("valid UI not extracted: %q %v", data, err)
			}
		})
	}
	if err := s.downloadZIP(bytes.NewReader(uiZIP(t, "repo/link", os.ModeSymlink|0o777)), t.TempDir()); err == nil {
		t.Fatal("archive symlink accepted")
	}
}

func TestExternalUIRejectsExistingSymlinkEscape(t *testing.T) {
	output, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(output, "assets")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := &Server{ctx: context.Background()}
	if err := s.downloadZIP(bytes.NewReader(uiZIP(t, "repo/assets/index.html", 0o644)), output); err == nil {
		t.Fatal("existing symlink escape accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "index.html")); !os.IsNotExist(err) {
		t.Fatalf("outside file created: %v", err)
	}
}
