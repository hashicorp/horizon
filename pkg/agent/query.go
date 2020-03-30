package agent

import (
	"github.com/hashicorp/horizon/pkg/edgeservices/agents"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/y0ssar1an/q"
)

func (a *Agent) RPCClient() (*wire.RPCClient, error) {
	a.mu.Lock()
	stream, err := a.sessions[0].OpenStream()
	a.mu.Unlock()

	if err != nil {
		return nil, err
	}

	return wire.NewRPCClient(stream), nil
}

func (a *Agent) QueryPeerService(labels []string) ([]*agents.Service, error) {
	rpc, err := a.RPCClient()
	if err != nil {
		return nil, err
	}

	var (
		req  agents.QueryRequest
		resp agents.QueryResponse
	)

	req.Labels = labels

	err = rpc.Call("agents.edge", "/query/peers", &req, &resp)
	if err != nil {
		return nil, err
	}

	q.Q(resp)

	return resp.Services, nil
}
