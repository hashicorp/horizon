package hub

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

func (h *Hub) handleAgentStream(ctx context.Context, stream *yamux.Stream, wctx wire.Context) {
	defer stream.Close()

	L := h.L

	L.Trace("stream accepted", "id", stream.StreamID())

	var req wire.Request

	tag, err := wctx.ReadRequest(&req)
	if err != nil {
		L.Error("error decoding request", "error", err)
		return
	}

	if tag != 1 {
		L.Error("incorrect message tag", "tag", tag)
		return
	}

	err = h.handleRequest(ctx, L, stream, wctx, &req)
	if err != nil {
		L.Error("error handling request", "error", err)
	}
}

type frameAccessor struct {
	*wire.FramingReader
	*wire.FramingWriter
}

func (fa *frameAccessor) WriteResponse(resp *wire.Response) error {
	_, err := fa.WriteMarshal(1, resp)
	return err
}

func (h *Hub) handleRequest(ctx context.Context, L hclog.Logger, stream *yamux.Stream, wctx wire.Context, req *wire.Request) error {
	L.Info("request started", "method", req.Method, "path", req.Path)

	serv, ok := h.services.Lookup(req.Host)
	if !ok {
		var resp wire.Response
		resp.Code = 404

		resp.Headers = append(resp.Headers, &wire.Header{
			Name:  "X-FailureReason",
			Value: []string{fmt.Sprintf("no known edge service: %s", req.Host)},
		})

		err := wctx.WriteResponse(1, &resp)
		return err
	}

	return serv.Handler.HandleRequest(ctx, L, wctx, req)
}
