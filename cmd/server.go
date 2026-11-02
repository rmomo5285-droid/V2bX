package cmd

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"github.com/InazumaV/V2bX/common/accesslog"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	config string
	watch  bool
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run V2bX server",
	Run:   serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/V2bX/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

func serverHandle(_ *cobra.Command, _ []string) {
	showVersion()
	c := conf.New()
	err := c.LoadFromPath(config)
	if err != nil {
		log.WithField("err", err).Error("Load config file failed")
		return
	}
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if c.LogConfig.Output != "" {
		f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.WithField("err", err).Error("Open log file failed, using stdout instead")
		} else {
			log.SetOutput(f)
			defer func() { log.SetOutput(os.Stderr); _ = f.Close() }()
		}
	}
	limiter.Init()
	statusPath := filepath.Join(filepath.Dir(config), "accesslog.status.json")
	if err := accesslog.Configure(c.AccessLog, statusPath); err != nil {
		log.WithField("err", err).Error("Invalid access log configuration")
		return
	}
	defer accesslog.Shutdown()
	log.Info("Start V2bX...")
	vc, err := vCore.NewCore(c.CoresConfig)
	if err != nil {
		log.WithField("err", err).Error("new core failed")
		return
	}
	err = vc.Start()
	if err != nil {
		log.WithField("err", err).Error("Start core failed")
		_ = vc.Close()
		return
	}
	log.Info("Core ", vc.Type(), " started")
	nodes := node.New()
	ctx, cancel := context.WithCancel(context.Background())
	var lifecycle sync.Mutex
	defer func() {
		cancel()
		lifecycle.Lock()
		defer lifecycle.Unlock()
		nodes.Close()
		_ = vc.Close()
	}()
	err = nodes.Start(c.NodeConfig, vc)
	if err != nil {
		log.WithField("err", err).Error("Run nodes failed")
		return
	}
	log.Info("Nodes started")
	xdns := os.Getenv("XRAY_DNS_PATH")
	sdns := os.Getenv("SING_DNS_PATH")
	if watch {
		err = c.WatchContext(ctx, config, xdns, sdns, func(next *conf.Conf) {
			lifecycle.Lock()
			defer lifecycle.Unlock()
			if ctx.Err() != nil {
				return
			}
			// Construct first, preserving the running instance on invalid core settings.
			replacement, buildErr := vCore.NewCore(next.CoresConfig)
			if buildErr != nil {
				log.WithError(buildErr).Error("Reload core configuration rejected")
				return
			}
			previous := c
			nodes.Close()
			if err := vc.Close(); err != nil {
				log.WithError(err).Warn("Close previous core failed")
			}
			vc = replacement
			restart := func(config *conf.Conf) error {
				if err := vc.Start(); err != nil {
					_ = vc.Close()
					return err
				}
				if err := nodes.Start(config.NodeConfig, vc); err != nil {
					_ = vc.Close()
					return err
				}
				return nil
			}
			if err := restart(next); err != nil {
				log.WithError(err).Error("Replacement failed; restoring previous configuration")
				restored, restoreErr := vCore.NewCore(previous.CoresConfig)
				if restoreErr == nil {
					vc = restored
					restoreErr = restart(previous)
				}
				if restoreErr != nil {
					log.WithError(restoreErr).Error("Previous configuration could not be restored")
				}
				return
			}
			c = next
			if err := accesslog.Configure(c.AccessLog, statusPath); err != nil {
				log.WithError(err).Error("Reload access log configuration failed")
			}
			log.Info("Nodes restarted")
			runtime.GC()
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			return
		}
	}
	// clear memory
	runtime.GC()
	// wait exit signal
	{
		osSignals := make(chan os.Signal, 1)
		signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(osSignals)
		<-osSignals
	}
}
