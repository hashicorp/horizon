package hub

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/pkg/errors"
)

type pivotAccountContext struct {
	wire.Context
	pa *pb.Account
}

func (p *pivotAccountContext) AccountId() *pb.ULID {
	return p.pa.AccountId
}

func (h *Hub) handleAgentStream(ctx context.Context, ai *agentConn, tkn *token.ValidToken, stream *yamux.Stream, wctx wire.Context) {
	defer stream.Close()
	defer func() {
		atomic.AddInt64(ai.TotalStreams, -1)
		h.sendAgentInfoFlow(ai)
	}()

	L := h.L

	L.Trace("stream accepted", "id", stream.StreamID())

	var req pb.ConnectRequest

	tag, err := wctx.ReadMarshal(&req)
	if err != nil {
		L.Error("error decoding request", "error", err)
		return
	}

	if tag != 1 {
		L.Error("incorrect message tag", "tag", tag)
		return
	}

	if req.PivotAccount != nil {
		if tkn.AllowAccount(req.PivotAccount.Namespace) {
			wctx = &pivotAccountContext{wctx, req.PivotAccount}
		} else {
			var resp pb.Response
			resp.Error = "invalid account pivot"
			wctx.WriteMarshal(255, &resp)
			return
		}
	}

	routes, err := h.cc.LookupService(ctx, tkn.AccountId(), req.Target)
	if err != nil {
		var resp pb.Response
		resp.Error = err.Error()
		wctx.WriteMarshal(255, &resp)
		return
	}

	for len(routes) > 0 {
		var target *pb.ServiceRoute

		target, routes, err = h.pickRoute(routes)
		if err != nil {
			var resp pb.Response
			resp.Error = err.Error()
			wctx.WriteMarshal(255, &resp)
			return
		}

		var fs pb.FlowStream
		fs.FlowId = pb.NewULID()
		fs.HubId = h.id
		fs.AgentId = ai.ID
		fs.ServiceId = target.Id
		fs.AccountId = tkn.AccountId()
		fs.Labels = req.Target
		fs.StartedAt = pb.NewTimestamp(time.Now())

		err = h.bridgeToTarget(ctx, ai, &fs, stream, target, &req, wctx)
		if err != nil {
			var resp pb.Response
			resp.Error = err.Error()
			wctx.WriteMarshal(255, &resp)
			return
		}
	}

	var resp pb.Response
	resp.Error = "no routes available to target"
	wctx.WriteMarshal(255, &resp)
}

func (h *Hub) pickRoute(routes []*pb.ServiceRoute) (*pb.ServiceRoute, []*pb.ServiceRoute, error) {
	return routes[0], routes[1:], nil
}

var ErrNoSuchSession = errors.New("no session found")

func (h *Hub) bridgeToTarget(
	ctx context.Context,
	ai *agentConn,
	fs *pb.FlowStream,
	stream *yamux.Stream,
	target *pb.ServiceRoute,
	req *pb.ConnectRequest,
	wctx wire.Context,
) error {
	// Oh look it's for me!
	if target.Hub.Equal(h.id) {
		h.mu.RLock()
		session, ok := h.active[target.Id.SpecString()]
		h.mu.RUnlock()

		if !ok {
			return ErrNoSuchSession
		}

		// transmit a ack back to the opener that the service was found and is
		// about to start.

		var conack pb.ConnectAck
		conack.ServiceId = target.Id

		err := wctx.WriteMarshal(1, &conack)
		if err != nil {
			return err
		}

		stream, err := session.OpenStream()
		if err != nil {
			return err
		}

		var sid pb.SessionIdentification
		sid.ServiceId = target.Id
		sid.ProtocolId = req.ProtocolId

		fw, err := wire.NewFramingWriter(stream)
		if err != nil {
			return err
		}

		defer fw.Recycle()

		_, err = fw.WriteMarshal(11, &sid)
		if err != nil {
			return err
		}

		fr, err := wire.NewFramingReader(stream)
		if err != nil {
			return err
		}

		defer fr.Recycle()

		dsctx := wire.NewContext(wctx.AccountId(), fr, fw)

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		statUpdate := time.NewTicker(time.Minute)
		defer statUpdate.Stop()

		go func() {
			var (
				exit         bool
				prevBytes    int64
				prevMessages int64
			)

			for {
				select {
				case <-ctx.Done():
					exit = true
					fs.EndedAt = pb.NewTimestamp(time.Now())
				case <-statUpdate.C:
					// ok
				}

				ma, ba := wctx.Accounting()
				mb, bb := dsctx.Accounting()

				// We only transmit the number of messages/bytes since the previous update.
				// This allows the server to treat the value as a counter rather than a gauge.
				// This is the same as NetFlow does it, where a flow will say how many packets
				// and the number of bytes those packets are represented by just that flow update.
				fs.NumMessages = (ma + mb) - prevMessages
				fs.NumBytes = (ba + bb) - prevBytes

				h.cc.SendFlow(&pb.FlowRecord{Stream: fs})

				if exit {
					return
				}
			}
		}()

		return wctx.BridgeTo(dsctx)
	}

	return ErrNoSuchSession
}

type frameAccessor struct {
	*wire.FramingReader
	*wire.FramingWriter
}

func (fa *frameAccessor) WriteResponse(resp *pb.Response) error {
	_, err := fa.WriteMarshal(1, resp)
	return err
}

/*
func (h *Hub) handleRequest(ctx context.Context, L hclog.Logger, stream *yamux.Stream, wctx wire.Context, req *pb.Request) error {
	L.Info("request started", "method", req.Method, "path", req.Path)

	h.cc.LookupService(ctx, wctx.AccountId(), req.Labels)

	serv, ok := h.services.Lookup(req.Host)
	if !ok {
		var resp wire.Response
		resp.Code = 404

		resp.Headers = append(resp.Headers, &wire.Header{
			Name:  "X-FailureReason",
			Value: []string{fmt.Sprintf("no known edge service: %s", req.Host)},
		})

		err := wctx.WriteMarshal(1, &resp)
		return err
	}

	return serv.Handler.HandleRequest(ctx, L, wctx, req)
}
*/
