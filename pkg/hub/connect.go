package hub

import (
	"context"
	"io"
	"net"

	"github.com/hashicorp/horizon/pkg/connect"
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/pb"
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
	// Oh look it's for me!
	if !target.Hub.Equal(h.id) {
		return h.connectToRemoteService(ctx, target, account, proto, token)
	}
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

	dsctx := wire.NewContext(account, fr, fw)

	return dsctx, nil
}

func (h *Hub) connectToRemoteService(
	ctx context.Context,
	target *pb.ServiceRoute,
	account *pb.Account,
	proto string,
	token string,
) (wire.Context, error) {
	L := ctxlog.L(ctx)

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

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}

	addr = net.JoinHostPort(host, port)

	L.Trace("spawning connection to peer hub", "hub", target.Hub, "addr", addr)

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

	return conn.WireContext(account), nil
}
