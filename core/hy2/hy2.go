package hy2

import (
	"errors"
	"sync"

	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"go.uber.org/zap"
)

var _ vCore.Core = (*Hysteria2)(nil)

type Hysteria2 struct {
	Hy2nodes map[string]Hysteria2node
	nodesMu  sync.RWMutex
	Auth     *V2bX
	Logger   *zap.Logger
}

func init() {
	vCore.RegisterCore("hysteria2", New)
}

func New(c *conf.CoreConfig) (vCore.Core, error) {
	loglever := "error"
	if c.Hysteria2Config.LogConfig.Level != "" {
		loglever = c.Hysteria2Config.LogConfig.Level
	}
	log, err := initLogger(loglever, "console", c.Hysteria2Config.LogConfig.Output)
	if err != nil {
		return nil, err
	}
	return &Hysteria2{
		Hy2nodes: make(map[string]Hysteria2node),
		Auth: &V2bX{
			usersMap: make(map[string]map[string]int),
		},
		Logger: log,
	}, nil
}

func (h *Hysteria2) Protocols() []string {
	return []string{
		"hysteria2",
	}
}

func (h *Hysteria2) Start() error {
	return nil
}

func (h *Hysteria2) Close() error {
	h.nodesMu.RLock()
	nodes := make([]Hysteria2node, 0, len(h.Hy2nodes))
	for _, n := range h.Hy2nodes {
		nodes = append(nodes, n)
	}
	h.nodesMu.RUnlock()
	var errs []error
	for _, n := range nodes {
		if err := n.Hy2server.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *Hysteria2) node(tag string) (Hysteria2node, bool) {
	h.nodesMu.RLock()
	node, ok := h.Hy2nodes[tag]
	h.nodesMu.RUnlock()
	return node, ok
}

func (h *Hysteria2) Type() string {
	return "hysteria2"
}
