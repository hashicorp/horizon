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

type ServiceConnector interface {
	ConnectToService(req *wire.Request, accid string, rs registry.ResolvedService) (wire.Context, error)
}

type ConnectService struct {
	reg *registry.Registry
	sc  ServiceConnector
}

func (c *ConnectService) HandleRPC(ctx context.Context, wctx wire.RPCContext) error {
	var req ConnectRequest

	err := wctx.ReadRequest(&req)
	if err != nil {
		return err
	}

	rs := registry.ResolvedService{
		Agent:       req.Target.SessionId,
		AgentKey:    req.Target.AgentKey,
		ServiceId:   req.Target.ServiceId,
		ServiceType: req.Target.Type,
	}

	var dreq wire.Request

	downstream, err := c.sc.ConnectToService(&dreq, wctx.AccountId(), rs)
	if err != nil {
		return err
	}

	return wctx.BridgeTo(downstream)
}

type AgentsService struct {
	wire.RPCServer
}

func NewService(reg *registry.Registry, sc ServiceConnector) *AgentsService {
	serv := &AgentsService{}
	serv.AddMethod("/query/peers", &PeersService{reg: reg})
	serv.AddMethod("/connect/peer", &ConnectService{reg: reg, sc: sc})

	return serv
}
