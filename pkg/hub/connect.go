package hub

import (
	"context"
	"io"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/pierrec/lz4/v3"
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

	return nil, ErrNoSuchSession
}
