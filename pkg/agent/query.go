package agent

import (
	"github.com/hashicorp/horizon/pkg/wire"
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

/*

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

	return resp.Services, nil
}

func (a *Agent) ConnectToPeer(serv *agents.Service) (wire.Context, error) {
	rpc, err := a.RPCClient()
	if err != nil {
		return nil, err
	}

	var req agents.ConnectRequest
	req.Target = serv

	wctx, err := rpc.Begin("agents.edge", "/connect/peer", &req)
	if err != nil {
		return nil, err
	}

	return wctx, nil
}

*/
