package hy2

import (
	"fmt"
	"strings"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type Hysteria2node struct {
	Hy2server     server.Server
	Tag           string
	Logger        *zap.Logger
	EventLogger   server.EventLogger
	TrafficLogger server.TrafficLogger
}

func (h *Hysteria2) AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error {
	var err error
	limit, err := limiter.GetLimiter(tag)
	if err != nil {
		return fmt.Errorf("get limiter for native Hysteria2 node %s: %w", tag, err)
	}
	for _, protocol := range limit.ProtocolRulesSnapshot() {
		if protocol != "bittorrent" {
			return fmt.Errorf("native Hysteria2 cannot enforce panel protocol rule %q; supported protocol rule: bittorrent", protocol)
		}
	}
	hyconfig := &server.Config{}
	var c serverConfig
	v := viper.New()
	if len(config.Hysteria2ConfigPath) != 0 {
		v.SetConfigFile(config.Hysteria2ConfigPath)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read native Hysteria2 config: %w", err)
		}
		if err := v.Unmarshal(&c); err != nil {
			return fmt.Errorf("parse native Hysteria2 config: %w", err)
		}
	}
	streamUsers := &hy2StreamUsers{}
	n := Hysteria2node{
		Tag:    tag,
		Logger: h.Logger,
		EventLogger: &serverLogger{
			Tag:    tag,
			logger: h.Logger,
		},
		TrafficLogger: &HookServer{
			Tag:                   tag,
			logger:                h.Logger,
			ReportMinTrafficBytes: config.ReportMinTraffic * 1024,
			StreamUsers:           streamUsers,
		},
	}

	hyconfig, err = n.getHyConfig(info, config, &c)
	if err != nil {
		return err
	}
	hyconfig.Authenticator = &nodeAuthenticator{
		tag:    tag,
		users:  h.Auth,
		logger: h.Logger,
	}
	s, err := server.NewServer(hyconfig)
	if err != nil {
		return err
	}
	n.Hy2server = s
	h.nodesMu.Lock()
	h.Hy2nodes[tag] = n
	h.nodesMu.Unlock()
	go func() {
		if err := s.Serve(); err != nil {
			if !strings.Contains(err.Error(), "quic: server closed") {
				h.Logger.Error("Server Error", zap.Error(err))
			}
		}
	}()
	return nil
}

func (h *Hysteria2) DelNode(tag string) error {
	h.nodesMu.Lock()
	node, ok := h.Hy2nodes[tag]
	if !ok {
		h.nodesMu.Unlock()
		return fmt.Errorf("native Hysteria2 node %s not found", tag)
	}
	err := node.Hy2server.Close()
	if err != nil {
		h.nodesMu.Unlock()
		return err
	}
	delete(h.Hy2nodes, tag)
	h.nodesMu.Unlock()
	h.Auth.mutex.Lock()
	delete(h.Auth.usersMap, tag)
	h.Auth.mutex.Unlock()
	return nil
}
