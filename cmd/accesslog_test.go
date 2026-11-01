package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/conf"
)

func TestAccessLogConfigurePreservesExistingNodeConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := []byte(`{"Nodes":[],"Cores":[],"custom_setting":"keep-me"}`)
	os.WriteFile(path, original, 0600)
	for _, secret := range []string{"first-test-secret", "second-test-secret"} {
		c := newAccessLogCommand()
		out := new(bytes.Buffer)
		c.SetOut(out)
		c.SetErr(out)
		c.SetIn(strings.NewReader(secret + "\n"))
		c.SetArgs([]string{"configure", "--config", path, "--endpoint", "https://custom.example.net/collect", "--username", "custom_writer", "--password-stdin"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), secret) {
			t.Fatal("secret printed")
		}
		cfg := conf.New()
		if err := cfg.LoadFromPath(path); err != nil {
			t.Fatal(err)
		}
		if !cfg.AccessLog.Enabled || cfg.AccessLog.Endpoint != "https://custom.example.net/collect" || cfg.AccessLog.Password != secret {
			t.Fatal("saved config not used")
		}
		main, _ := os.ReadFile(path)
		if !bytes.Equal(main, original) {
			t.Fatal("main node config rewritten")
		}
	}
	previous, err := os.ReadFile(filepath.Join(dir, "accesslog.json.previous"))
	if err != nil || !bytes.Contains(previous, []byte("first-test-secret")) {
		t.Fatal("bounded rollback config missing")
	}
}
