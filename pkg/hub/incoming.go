package hub

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hashicorp/horizon/pkg/connect"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/pierrec/lz4/v3"
	"github.com/pkg/errors"
)

type pivotAccountContext struct {
	wire.Context
	pa *pb.Account
}

func (p *pivotAccountContext) AccountId() *pb.ULID {
	return p.pa.AccountId
}

func (h *Hub) handleAgentStream(ctx context.Context, ai *agentConn, stream *yamux.Stream, wctx wire.Context) {
	defer stream.Close()
	defer func() {
		atomic.AddInt64(ai.TotalStreams, -1)
		h.sendAgentInfoFlow(ai)
	}()

	L := h.L

	L.Trace("stream accepted", "hub", h.id, "id", stream.StreamID())
	defer L.Trace("stream ended", "id", stream.StreamID())

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
		if ai.token.AllowAccount(req.PivotAccount.Namespace) {
			wctx = &pivotAccountContext{wctx, req.PivotAccount}
		} else {
			var resp pb.Response
			resp.Error = "invalid account pivot"
			wctx.WriteMarshal(255, &resp)
			return
		}
	}

	routes, err := h.cc.LookupService(ctx, wctx.Account(), req.Target)
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
		fs.Account = wctx.Account()
		fs.Labels = req.Target
		fs.StartedAt = pb.NewTimestamp(time.Now())

		err = h.bridgeToTarget(ctx, ai, &fs, target, &req, wctx)
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
	target *pb.ServiceRoute,
	req *pb.ConnectRequest,
	wctx wire.Context,
) error {
	L := h.L

	L.Trace("bridging connection to hub", "from", h.id, "to", target.Hub)

	// Oh look it's for me!
	if !target.Hub.Equal(h.id) {
		return h.forwardToTarget(ctx, ai, fs, target, req, wctx)
	}

	h.mu.RLock()
	ac, ok := h.active[target.Id.SpecString()]
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

	stream, err := ac.session.OpenStream()
	if err != nil {
		return err
	}

	h.L.Trace("connecting to agent", "agent", ai.ID, "service", target.Id, "lz4", ac.useLZ4)

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
	sid.ProtocolId = req.ProtocolId

	fw, err := wire.NewFramingWriter(w)
	if err != nil {
		return err
	}

	defer fw.Recycle()

	_, err = fw.WriteMarshal(11, &sid)
	if err != nil {
		return err
	}

	fr, err := wire.NewFramingReader(r)
	if err != nil {
		return err
	}

	defer fr.Recycle()

	dsctx := wire.NewContext(wctx.Account(), fr, fw)

	return h.copyBetweenContexts(ctx, wctx, dsctx, fs, ai)
}

func isPublic(labels *pb.LabelSet) bool {
	if labels == nil {
		return true
	}

	for _, lbl := range labels.Labels {
		if lbl.Name == "type" {
			switch lbl.Value {
			case "public":
				return true
			case "private":
				return false
			default:
				return false
			}
		}
	}

	// No type tag, consider public
	return true
}

func findPrio(loc *pb.NetworkLocation) int {
	if loc.Labels == nil {
		return 0
	}

	for _, lbl := range loc.Labels.Labels {
		if lbl.Name == "priority" {
			if i, err := strconv.Atoi(lbl.Value); err == nil {
				return i
			}
		}
	}

	return 0
}

func pickBestOf(locs []*pb.NetworkLocation) (string, error) {
	if len(locs) == 1 {
		return locs[0].Addresses[0], nil
	}

	prio := 0
	var pick *pb.NetworkLocation

	for _, loc := range locs {
		tp := findPrio(loc)

		if pick == nil || tp > prio {
			pick = loc
			prio = tp
		}
	}

	return pick.Addresses[0], nil
}

var ErrNoAvailableAddresses = errors.New("no addresses available for hub")

// Given a list of network locations, pick one to connect to and return
// an address.
func (h *Hub) pickAddress(locs []*pb.NetworkLocation) (string, error) {
	// TODO: figure out a heiristic for when a location has multiple addresses
	// and when we should use them. Maybe some backoff logic associated with each one?

	switch len(locs) {
	case 0:
		return "", nil
	case 1:
		return locs[0].Addresses[0], nil
	}

	var (
		candidate []*pb.NetworkLocation
		fallback  []*pb.NetworkLocation
		publics   []*pb.NetworkLocation
	)

	for _, ploc := range locs {
		if isPublic(ploc.Labels) {
			publics = append(publics, ploc)
		}

		for _, sloc := range h.location {
			if ploc.Labels != nil && ploc.Labels.Len() > 0 && sloc.Labels != nil && sloc.Labels.Len() > 0 {
				if ploc.Labels.Equal(sloc.Labels) {
					if isPublic(ploc.Labels) {
						fallback = append(fallback, ploc)
					} else {
						candidate = append(candidate, ploc)
					}
				}
			}
		}
	}

	if len(candidate) > 0 {
		return pickBestOf(candidate)
	}

	if len(fallback) > 0 {
		return pickBestOf(fallback)
	}

	if len(publics) > 0 {
		return pickBestOf(publics)
	}

	// if all else fails, just pick the first one.

	return locs[0].Addresses[0], nil
}

func (h *Hub) forwardToTarget(
	ctx context.Context,
	ai *agentConn,
	fs *pb.FlowStream,
	target *pb.ServiceRoute,
	req *pb.ConnectRequest,
	wctx wire.Context,
) error {
	L := h.L

	locs, err := h.cc.GetHubAddresses(ctx, target.Hub)
	if err != nil {
		L.Error("error fetching locations for target hub", "hub", target.Hub)
		return err
	}

	if len(locs) == 0 {
		L.Error("no locations for target hub", "hub", target.Hub)
		return ErrNoSuchSession
	}

	L.Trace("locations for target hub", "hub", target.Hub, "locations", locs)

	addr, err := h.pickAddress(locs)
	if err != nil {
		return err
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}

	addr = net.JoinHostPort(host, port)

	L.Trace("spawning connection to peer hub", "hub", target.Hub, "addr", addr)

	session, err := connect.Connect(L, addr, ai.stoken)
	if err != nil {
		return err
	}

	// We're allowing the target hub to do it's own lookup again rather than
	// passing the service id we calculated here. The advantage is that things
	// might have changed and the target has a better target (which would result
	// in multiple relays).
	conn, err := session.ConnecToService(req.Target)
	if err != nil {
		return err
	}

	dsctx := conn.WireContext(wctx.Account())

	// transmit a ack back to the opener that the service was found and is
	// about to start.

	var conack pb.ConnectAck
	conack.ServiceId = target.Id

	err = wctx.WriteMarshal(1, &conack)
	if err != nil {
		return err
	}

	// We don't send a SessionIdentification here because the target
	// hub will send it's own when connecting to the agent.

	return h.copyBetweenContexts(ctx, wctx, dsctx, fs, ai)
}

func (h *Hub) copyBetweenContexts(ctx context.Context, wctx, dsctx wire.Context, fs *pb.FlowStream, ai *agentConn) error {
	h.L.Trace("copying data between contexts for agent", "id", ai.ID)

	start := time.Now()

	defer func() {
		h.L.Trace("finished context data copy", "id", ai.ID, "duration", time.Since(start))
	}()

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

			fs.Duration = int64(time.Since(start))

			h.cc.SendFlow(&pb.FlowRecord{Stream: fs})

			if exit {
				return
			}
		}
	}()

	return wctx.BridgeTo(dsctx)
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
