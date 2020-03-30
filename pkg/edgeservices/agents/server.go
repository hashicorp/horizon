package agents

import (
	"context"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/registry"
	"github.com/hashicorp/horizon/pkg/wire"
)

type PeersService struct {
	reg *registry.Registry
}

func (p *PeersService) HandleRPC(ctx context.Context, wctx wire.RPCContext) error {
	var req QueryRequest

	err := wctx.ReadRequest(&req)
	if err != nil {
		return err
	}

	L := hclog.L()

	services, err := p.reg.MatchServices(wctx.AccountId(), req.Labels)
	if err != nil {
		return err
	}

	L.Info("querying matching services", "account", wctx.AccountId(), "labels", req.Labels, "services", len(services))

	var resp QueryResponse

	for _, serv := range services {
		resp.Services = append(resp.Services, &Service{
			SessionId: serv.Agent,
			ServiceId: serv.ServiceId,
			Type:      serv.ServiceType,
			AgentKey:  serv.AgentKey,
		})
	}

	return wctx.WriteResponse(&resp)
}

type AgentsService struct {
	wire.RPCServer
}

func NewService(reg *registry.Registry) *AgentsService {
	serv := &AgentsService{}
	serv.AddMethod("/query/peers", &PeersService{reg: reg})

	return serv
}
