package conf

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/InazumaV/V2bX/common/accesslog"
	"github.com/InazumaV/V2bX/common/json5"

	"encoding/json/v2"
)

type Conf struct {
	AccessLog   accesslog.Config `json:"AccessLog"`
	LogConfig   LogConfig        `json:"Log"`
	CoresConfig []CoreConfig     `json:"Cores"`
	NodeConfig  []NodeConfig     `json:"Nodes"`
}

func New() *Conf {
	return &Conf{
		LogConfig: LogConfig{
			Level:  "info",
			Output: "",
		},
	}
}

func (p *Conf) LoadFromPath(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open config file error: %s", err)
	}
	defer f.Close()

	reader := json5.NewTrimNodeReader(f)
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read config file error: %s", err)
	}

	err = json.Unmarshal(data, p)
	if err != nil {
		return fmt.Errorf("unmarshal config error: %s", err)
	}

	// A sidecar lets existing installations opt in without rewriting their
	// main config, included nodes, credentials, comments or custom core settings.
	sidecar := filepath.Join(filepath.Dir(filePath), "accesslog.json")
	data, err = os.ReadFile(sidecar)
	if err == nil {
		p.AccessLog = accesslog.Config{}
		if err := json.Unmarshal(data, &p.AccessLog); err != nil {
			return fmt.Errorf("decode accesslog.json: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read accesslog.json: %w", err)
	}
	if p.AccessLog.PasswordFile != "" && !filepath.IsAbs(p.AccessLog.PasswordFile) {
		p.AccessLog.PasswordFile = filepath.Join(filepath.Dir(filePath), p.AccessLog.PasswordFile)
	}
	return nil
}
