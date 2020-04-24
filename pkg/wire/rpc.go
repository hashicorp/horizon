package wire

import (
	"context"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/yamux"
	"github.com/pkg/errors"
)

type RPCClient struct {
	stream *yamux.Stream
}

const rpcTag = 20

func (r *RPCClient) Begin(host, path string, req Marshaller) (RPCContext, error) {
	var wreq pb.Request
	wreq.Type = pb.RPC
	wreq.Path = path
	wreq.Host = host

	fw, err := NewFramingWriter(r.stream)
	if err != nil {
		return nil, err
	}

	_, err = fw.WriteMarshal(1, &wreq)
	if err != nil {
		return nil, err
	}

	_, err = fw.WriteMarshal(rpcTag, req)
	if err != nil {
		return nil, err
	}

	fr, err := NewFramingReader(r.stream)
	if err != nil {
		return nil, err
	}

	return &rpcCtx{
		Context: &ctx{
			fr: fr,
			fw: fw,
		},
	}, nil
}

func (r *RPCClient) Call(host, path string, req Marshaller, resp Unmarshaller) error {
	var wreq pb.Request
	wreq.Type = pb.RPC
	wreq.Path = "/query/peers"
	wreq.Host = "agents.edge"

	fw, err := NewFramingWriter(r.stream)
	if err != nil {
		return err
	}

	defer fw.Recycle()

	_, err = fw.WriteMarshal(1, &wreq)
	if err != nil {
		return err
	}

	_, err = fw.WriteMarshal(rpcTag, req)
	if err != nil {
		return err
	}

	fr, err := NewFramingReader(r.stream)
	if err != nil {
		return err
	}

	defer fr.Recycle()

	tag, _, err := fr.ReadMarshal(resp)
	if err != nil {
		return err
	}

	if tag != rpcTag {
		return errors.Wrapf(ErrProtocolError, "wrong tag recieved: %d", tag)
	}

	return nil
}

func NewRPCClient(stream *yamux.Stream) *RPCClient {
	return &RPCClient{
		stream: stream,
	}
}

type RPCContext interface {
	Context
	ReadRequest(v Unmarshaller) error
	WriteResponse(v Marshaller) error
}

type RPCHandler interface {
	HandleRPC(ctx context.Context, wctx RPCContext) error
}

type RPCServer struct {
	mu      sync.RWMutex
	methods map[string]RPCHandler
}

func (r *RPCServer) AddMethod(name string, h RPCHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.methods == nil {
		r.methods = make(map[string]RPCHandler)
	}

	r.methods[name] = h
}

var ErrUnknownMethod = errors.New("unknown method requested")

type ReadMarshaler interface {
	ReadMarshal(Unmarshaller) (byte, int, error)
}

type WriteMarshaler interface {
	WriteMarshal(byte, Marshaller) (int, error)
}

type rpcCtx struct {
	Context
}

func (r *rpcCtx) ReadRequest(v Unmarshaller) error {
	tag, err := r.Context.ReadMarshal(v)
	if err != nil {
		return err
	}

	if tag != rpcTag {
		return errors.Wrapf(ErrProtocolError, "incorrect tag: %d (expected %d)", tag, rpcTag)
	}

	return nil
}

func (r *rpcCtx) WriteResponse(v Marshaller) error {
	return r.Context.WriteMarshal(rpcTag, v)
}

func (r *RPCServer) HandleRequest(ctx context.Context, L hclog.Logger, wctx Context, wreq *pb.Request) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	handler, ok := r.methods[wreq.Path]
	if !ok {
		return errors.Wrapf(ErrUnknownMethod, "no handler for method: %s", wreq.Path)
	}

	return handler.HandleRPC(ctx, &rpcCtx{wctx})
}
