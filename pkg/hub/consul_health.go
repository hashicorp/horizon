package hub

import (
	"context"
	"sync"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

var (
	// The maximum time to run a blocking query to the catalog for.
	DefaultRefreshTime = 30 * time.Second

	// How long to configured the consul TTL check with. This is the maximum time
	// the service has to update it's TTL again before it's marked as failed.
	CheckTTL = "30s"

	// ConsulService is the service name that is used to identify hubs. Each
	// instance of ConsulHealth identifies itself as a separate instance of this service.
	ConsulService = "hub"
)

// ConsulHealth advertises an instance of the hub service to consul and also
// monitors the service catalog for other instances. It provides the ability for
// each hub to know which other hubs are currently available to send traffic to.
type ConsulHealth struct {
	api *consul.Client
	id  string

	mu   sync.RWMutex
	hubs map[string]struct{}
}

// NewConsulHealth creates a ConsulHealth instance, identified by id.
func NewConsulHealth(id string, cfg *consul.Config) (*ConsulHealth, error) {
	c, err := consul.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	hubs := make(map[string]struct{})

	return &ConsulHealth{id: id, api: c, hubs: hubs}, nil
}

// Watch the hub consul service and update the local cache of that information,
// adding and removing other instances. If waitIndex is greater than 0, then
// this executes a blocking query to allow it to react to catalog changes.
func (c *ConsulHealth) refreshHubs(ctx context.Context, waitIndex uint64, waitTime time.Duration) (uint64, error) {
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
		c.hubs[serv.ServiceID] = struct{}{}
	}

	return qm.LastIndex, nil
}

// Watch runs forever, updating the local view of all hub instances. refreshTime
// controls how often the refresh queries wait for updates before trying again.
// The lower the value, the more this function will loop internally.
func (c *ConsulHealth) Watch(ctx context.Context, refreshTime time.Duration) {
	idx, _ := c.refreshHubs(ctx, 0, 0)

	for {
		idx, _ = c.refreshHubs(ctx, idx, refreshTime)
	}
}

// Start registers the service, begins updating the TTL check, and runs Watch to
// update the local catalog view in the background.
func (c *ConsulHealth) Start(ctx context.Context, L hclog.Logger) error {
	checkId, err := c.registerService(ctx)
	if err != nil {
		return err
	}

	dur, err := time.ParseDuration(CheckTTL)
	if err != nil {
		return err
	}

	// update the CheckTTL twice as fast as it expires, just to be sure we make it.
	updateTTL := dur / 2

	go func() {
		ticker := time.NewTicker(updateTTL)
		defer ticker.Stop()
		defer c.deregisterService(context.Background())

		for {
			select {
			case <-ticker.C:
				err := c.passCheck(checkId)
				if err != nil {
					L.Error("error updating consul check", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go c.Watch(ctx, DefaultRefreshTime)

	return nil
}

// registerService informs consul about a hub service instance. A TTL check
// is created for the service that should be updated using passCheck().
func (c *ConsulHealth) registerService(ctx context.Context) (string, error) {
	checkId := c.id + "-ttl"

	err := c.api.Agent().ServiceRegister(&consul.AgentServiceRegistration{
		Name: ConsulService,
		ID:   c.id,
		Check: &consul.AgentServiceCheck{
			CheckID: checkId,
			TTL:     CheckTTL,
		},
	})
	if err != nil {
		return "", err
	}

	return checkId, nil
}

// passCheck informs consul that the service is still alive.
func (c *ConsulHealth) passCheck(checkid string) error {
	c.api.Agent().UpdateTTL(checkid, "", "running")
	return nil
}

// deregisterService removes the consul service.
func (c *ConsulHealth) deregisterService(ctx context.Context) error {
	return c.api.Agent().ServiceDeregister(c.id)
}

// Available checks the local catalog cache and indicates if the given
// hub is currently available.
func (c *ConsulHealth) Available(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, in := c.hubs[id]
	return in
}
