package control

import (
	context "context"
	"sync"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

var (
	// The time to run a blocking query to the catalog for.
	RefreshTime = 30 * time.Second

	// ConsulService is the service name that is used to identify hubs. Each
	// instance of ConsulHealth identifies itself as a separate instance of this service.
	ConsulService = "hub"
)

// ConsulMonitor is a cousin to hub.ConsulHealth. It maintains a local cache of
// hub addresses for use when a control instance wishes to make a call to a hub.
type ConsulMonitor struct {
	L   hclog.Logger
	api *consul.Client

	mu   sync.RWMutex
	hubs map[string]string
}

var _ HubCatalog = (*ConsulMonitor)(nil)

// NewConsulMonitor connects to consul and returns a new value.
func NewConsulMonitor(L hclog.Logger, cfg *consul.Config) (*ConsulMonitor, error) {
	c, err := consul.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	cm := &ConsulMonitor{
		L:    L,
		api:  c,
		hubs: make(map[string]string),
	}

	return cm, nil
}

// Watch is meant to run in background, continously updating it's local cache
// using consul blocking queries against the consul catalog.
func (c *ConsulMonitor) Watch(ctx context.Context) {
	idx, _ := c.refreshHubs(ctx, 0, 0)

	for {
		idx, _ = c.refreshHubs(ctx, idx, RefreshTime)
	}
}

// Lookup returns the address of the requested hub.
func (c *ConsulMonitor) Lookup(id string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	addr, ok := c.hubs[id]
	return addr, ok
}

// Targets returns the address of all the hubs
func (c *ConsulMonitor) Targets() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tgs := make([]string, 0, len(c.hubs))

	for _, v := range c.hubs {
		tgs = append(tgs, v)
	}

	return tgs
}

// Watch the hub consul service and update the local cache of that information,
// adding and removing other instances. If waitIndex is greater than 0, then
// this executes a blocking query to allow it to react to catalog changes.
func (c *ConsulMonitor) refreshHubs(ctx context.Context, waitIndex uint64, waitTime time.Duration) (uint64, error) {
	var q consul.QueryOptions

	q.WaitIndex = waitIndex
	q.WaitTime = waitTime

	// TODO(evanphx) When we move to federated consul, we need to teach this code to run
	// refreshHubs against all the datacenters individually.

	services, qm, err := c.api.Catalog().Service(ConsulService, "", q.WithContext(ctx))
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var toDelete []string

top:
	for k := range c.hubs {
		for _, serv := range services {
			if k == serv.ServiceID {
				continue top
			}
		}

		toDelete = append(toDelete, k)
	}

	for _, k := range toDelete {
		delete(c.hubs, k)
	}

	for _, serv := range services {
		c.hubs[serv.ServiceID] = serv.ServiceAddress
		c.L.Info("hub added", "id", serv.ServiceID, "addr", serv.ServiceAddress)
	}

	c.L.Info("refreshed hubs", "cached", len(c.hubs), "in-consul", len(services))

	return qm.LastIndex, nil
}
