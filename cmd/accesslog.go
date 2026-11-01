package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/InazumaV/V2bX/common/accesslog"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func init() { command.AddCommand(newAccessLogCommand()) }

func newAccessLogCommand() *cobra.Command {
	var configPath, endpoint, username, backend, database, table string
	var passwordStdin bool
	root := &cobra.Command{Use: "accesslog", Short: "Configure optional access reporting without changing node settings"}
	root.PersistentFlags().StringVar(&configPath, "config", "/etc/V2bX/config.json", "main config location (sidecar uses the same directory)")
	configure := &cobra.Command{Use: "configure", Args: cobra.NoArgs, Short: "Save accesslog.json; read password without putting it in process arguments", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := accesslog.Config{Enabled: true, Endpoint: endpoint, Username: username, Backend: backend, Database: database, Table: table}
		if passwordStdin {
			b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4097))
			if err != nil || len(b) > 4096 {
				return fmt.Errorf("read password failed or password too long")
			}
			cfg.Password = strings.TrimRight(string(b), "\r\n")
		} else {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("use an interactive terminal, --password-stdin, or a protected PasswordFile config")
			}
			fmt.Fprint(cmd.ErrOrStderr(), "Access log writer password: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.ErrOrStderr())
			if err != nil {
				return fmt.Errorf("read password failed")
			}
			cfg.Password = string(b)
			clear(b)
		}
		if _, err := cfg.Prepare(); err != nil {
			return err
		}
		path := filepath.Join(filepath.Dir(configPath), "accesslog.json")
		if err := saveAccessLogConfig(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved %s (0600). Node settings unchanged. Restart V2bX to apply.\n", path)
		return nil
	}}
	configure.Flags().StringVar(&endpoint, "endpoint", "", "HTTPS ingest URL or ClickHouse HTTP URL")
	configure.Flags().StringVar(&username, "username", "", "write-only database username")
	configure.Flags().StringVar(&backend, "backend", "ingest", "ingest or clickhouse_http")
	configure.Flags().StringVar(&database, "database", "default", "database for clickhouse_http")
	configure.Flags().StringVar(&table, "table", "access_log", "table for clickhouse_http")
	configure.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin instead of terminal")
	_ = configure.MarkFlagRequired("endpoint")
	_ = configure.MarkFlagRequired("username")
	root.AddCommand(configure)
	root.AddCommand(&cobra.Command{Use: "disable", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := saveAccessLogConfig(filepath.Join(filepath.Dir(configPath), "accesslog.json"), accesslog.Config{}); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Access reporting disabled in config. Restart V2bX to apply.")
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		f, err := os.Open(filepath.Join(filepath.Dir(configPath), "accesslog.status.json"))
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(cmd.OutOrStdout(), bufio.NewReader(io.LimitReader(f, 8192)))
		fmt.Fprintln(cmd.OutOrStdout())
		return err
	}})
	return root
}

func saveAccessLogConfig(path string, cfg accesslog.Config) error {
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("accesslog.json must be a regular file")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err = writePrivateConfig(path+".previous", old); err != nil {
			return err
		}
	}
	return writePrivateConfig(path, append(data, '\n'))
}

// Same-directory rename also replaces a stale backup symlink rather than
// following it, and guarantees mode 0600 even if the old backup was public.
func writePrivateConfig(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".accesslog-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
