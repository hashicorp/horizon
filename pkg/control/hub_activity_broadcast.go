package control

import (
	context "context"

	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
)

type HubBroadcastActivity struct {
	cm *ConsulMonitor
	gd *GRPCDial
	br *Broadcaster
}

// NewHubBroadcastActivity creates a value that broadcasts AccountService values
// to all hubs registered in consul.
func NewHubBroadcastActivity(L hclog.Logger, token string, cfg *consul.Config, cert []byte) (*HubBroadcastActivity, error) {
	cm, err := NewConsulMonitor(L, cfg)
	if err != nil {
		return nil, err
	}

	gd, err := NewGRPCDial(token, cert)
	if err != nil {
		return nil, err
	}

	br, err := NewBroadcaster(L, cm, gd.Dial)
	if err != nil {
		return nil, err
	}

	hba := &HubBroadcastActivity{
		cm: cm,
		gd: gd,
		br: br,
	}

	return hba, nil
}

// Given a hub's id as a ULID, detect if that hub is actually available. This is used to prune
// references to hubs to include those that are available.
func (h *HubBroadcastActivity) HubAvailable(id *pb.ULID) bool {
	_, ok := h.cm.Lookup(id.SpecString())
	return ok
}

// Watch runs ConsulMonitor.Watch to update the local cache
func (h *HubBroadcastActivity) Watch(ctx context.Context) {
	h.cm.Watch(ctx)
}

// AdvertiseServices sends the given AccountServices to all registered
// hubs in consul.
func (h *HubBroadcastActivity) AdvertiseServices(ctx context.Context, acc *pb.AccountServices) error {
	return h.br.AdvertiseServices(ctx, acc)
}

// AdvertiseLabelLinks sends the given LabelLinks to all registered
// hubs in consul.
func (h *HubBroadcastActivity) AdvertiseLabelLinks(ctx context.Context, ll *pb.LabelLinks) error {
	return h.br.AdvertiseLabelLinks(ctx, ll)
}
