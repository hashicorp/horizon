package hub

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/hashicorp/horizon/pkg/connect"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/timing"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/pierrec/lz4/v3"
	"github.com/pkg/errors"
)

var ErrNoRoutes = errors.New("no routes to any service available")

func (h *Hub) ConnectToService(
	ctx context.Context,
	target *pb.ServiceRoute,
	account *pb.Account,
	proto string,
	token string,
) (wire.Context, error) {
	var (
		wctx wire.Context
		err  error
	)

	// Oh look it's not for me!
	if !target.Hub.Equal(h.id) {
		wctx, err = h.connectToRemoteService(ctx, target, account, proto, token)
		if err != nil {
			return nil, err
		}
	} else {
		defer timing.Track(ctx, "connect-local").Stop()

		h.mu.RLock()
		ac, ok := h.active[target.Id.SpecString()]
		h.mu.RUnlock()

		if !ok {
			return nil, ErrNoSuchSession
		}

		stream, err := ac.session.OpenStream()
		if err != nil {
			return nil, err
		}

		var (
			r io.Reader = stream
			w io.Writer = stream
		)

		if ac.useLZ4 {
			r = lz4.NewReader(stream)
			w = lz4.NewWriter(stream)
		}

		var sid pb.SessionIdentification
		sid.ServiceId = target.Id
		sid.ProtocolId = proto

		fw, err := wire.NewFramingWriter(w)
		if err != nil {
			return nil, err
		}

		_, err = fw.WriteMarshal(11, &sid)
		if err != nil {
			return nil, err
		}

		fr, err := wire.NewFramingReader(r)
		if err != nil {
			return nil, err
		}

		wctx = wire.NewContext(account, fr, fw)
	}

	sub, cancel := context.WithCancel(ctx)

	flowId := pb.NewULID()

	h.L.Trace("launching flow tracking goroutine for connect session", "id", flowId)

	go func() {
		start := time.Now()

		var fs pb.FlowStream
		fs.FlowId = flowId
		fs.HubId = h.id
		fs.AgentId = h.id
		fs.ServiceId = target.Id
		fs.Account = account
		fs.Labels = target.Labels
		fs.StartedAt = pb.NewTimestamp(start)

		statUpdate := time.NewTicker(time.Minute)
		defer statUpdate.Stop()

		var (
			exit         bool
			prevBytes    int64
			prevMessages int64
		)

		for {
			select {
			case <-sub.Done():
				exit = true
				fs.EndedAt = pb.NewTimestamp(time.Now())

				h.L.Trace("closing connection session flow tracking", "id", flowId)

			case <-statUpdate.C:
				// ok
			}

			ma, ba := wctx.Accounting()

			// We only transmit the number of messages/bytes since the previous update.
			// This allows the server to treat the value as a counter rather than a gauge.
			// This is the same as NetFlow does it, where a flow will say how many packets
			// and the number of bytes those packets are represented by just that flow update.
			fs.NumMessages = ma - prevMessages
			fs.NumBytes = ba - prevBytes

			fs.Duration = int64(time.Since(start))

			h.L.Trace("transmissing flow stream", "id", flowId)
			h.cc.SendFlow(&pb.FlowRecord{Stream: &fs})

			if exit {
				h.L.Trace("final flow stream data", "id", flowId, "messages", ma, "bytes", ba)
				return
			}
		}
	}()

	wrapped := wire.WithCloser(wctx, func() error { cancel(); return nil })

	return wrapped, nil
}

func (h *Hub) connectToRemoteService(
	ctx context.Context,
	target *pb.ServiceRoute,
	account *pb.Account,
	proto string,
	token string,
) (wire.Context, error) {
	defer timing.Track(ctx, "connect-remote").Stop()

	L := h.L

	locs, err := h.cc.GetHubAddresses(ctx, target.Hub)
	if err != nil {
		L.Error("error fetching locations for target hub", "hub", target.Hub)
		return nil, err
	}

	if len(locs) == 0 {
		L.Error("no locations for target hub", "hub", target.Hub)
		return nil, ErrNoSuchSession
	}

	L.Trace("locations for target hub", "hub", target.Hub, "locations", locs)

	addr, err := h.pickAddress(locs)
	if err != nil {
		return nil, err
	}

	L.Trace("picked target address", "address", addr)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}

	addr = net.JoinHostPort(host, port)

	L.Trace("spawning connection to peer hub", "hub", target.Hub, "addr", addr)

	// TODO: rather than spinning up a new session each time, use a connection
	// pool.
	session, err := connect.Connect(L, addr, token)
	if err != nil {
		return nil, err
	}

	// We're allowing the target hub to do it's own lookup again rather than
	// passing the service id we calculated here. The advantage is that things
	// might have changed and the target has a better target (which would result
	// in multiple relays).
	conn, err := session.ConnecToAccountService(account, target.Labels)
	if err != nil {
		return nil, err
	}

	return wire.WithCloser(conn.WireContext(account), session.Close), nil
}
