package hub

import (
	"context"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/pkg/errors"
)

var ErrNoRoutes = errors.New("no routes to any service available")

func (h *Hub) ConnectToService(
	ctx context.Context,
	target *pb.ServiceRoute,
	account *pb.ULID,
	proto string,
) (wire.Context, error) {
	// Oh look it's for me!
	if target.Hub.Equal(h.id) {
		h.mu.RLock()
		session, ok := h.active[target.Id.SpecString()]
		h.mu.RUnlock()

		if !ok {
			return nil, ErrNoSuchSession
		}

		stream, err := session.OpenStream()
		if err != nil {
			return nil, err
		}

		var sid pb.SessionIdentification
		sid.ServiceId = target.Id
		sid.ProtocolId = proto

		fw, err := wire.NewFramingWriter(stream)
		if err != nil {
			return nil, err
		}

		_, err = fw.WriteMarshal(11, &sid)
		if err != nil {
			return nil, err
		}

		fr, err := wire.NewFramingReader(stream)
		if err != nil {
			return nil, err
		}

		dsctx := wire.NewContext(account, fr, fw)

		return dsctx, nil
	}

	return nil, ErrNoSuchSession
}
