package discovery

import (
	"context"
	"crypto/x509"
	"sync"
)

type HubConfig struct {
	Addr       string
	Name       string
	Insecure   bool
	PinnedCert *x509.Certificate
}

type HubConnectDetails interface {
	Address() string
	InsecureTLS() bool
	X509Cert() *x509.Certificate
}

type HubConfigProvider interface {
	Take(context.Context) (HubConfig, bool)
	Return(HubConfig)
}

type StaticHubConfigs struct {
	mu      sync.Mutex
	configs []HubConfig
}

func HubConfigs(cfg ...HubConfig) HubConfigProvider {
	return &StaticHubConfigs{configs: cfg}
}

func (h *StaticHubConfigs) Take(ctx context.Context) (HubConfig, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.configs) == 0 {
		return HubConfig{}, false
	}

	cfg := h.configs[0]
	h.configs = h.configs[1:]

	return cfg, true
}

func (h *StaticHubConfigs) Return(cfg HubConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.configs = append(h.configs, cfg)
}
