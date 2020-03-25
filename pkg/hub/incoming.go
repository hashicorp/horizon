package hub

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

func (h *Hub) handleAgentStream(ctx context.Context, stream *yamux.Stream) {
	defer stream.Close()

	L := h.L

	L.Trace("stream accepted", "id", stream.StreamID())

	fr, err := wire.NewFramingReader(stream)
	if err != nil {
		L.Error("error creating frame reader", "error", err)
		return
	}

	defer fr.Recycle()

	fw, err := wire.NewFramingWriter(stream)
	if err != nil {
		L.Error("error creating framing writer", "error", err)
		return
	}

	defer fw.Recycle()

	var req wire.Request

	tag, _, err := fr.ReadMarshal(&req)
	if err != nil {
		L.Error("error decoding request", "error", err)
		return
	}

	if tag != 1 {
		L.Error("incorrect message tag", "tag", tag)
		return
	}

	switch req.Type {
	case wire.HTTP:
		err = h.handleHTTP(ctx, L, stream, fr, fw, &req)
		if err != nil {
			L.Error("error processing http request", "error", err)
		}
	default:
		L.Error("unknown request type detected", "type", req.Type)

		var resp wire.Response
		resp.Error = "unknown type"

		_, err = fw.WriteMarshal(1, &resp)
		if err != nil {
			L.Error("error marshaling response", "error", err)
			return
		}
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

func (h *Hub) handleHTTP(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.FramingReader, fw *wire.FramingWriter, req *wire.Request) error {
	L.Info("request started", "method", req.Method, "path", req.Path)

	serv, ok := h.services.Lookup(req.Host)
	if !ok {
		var resp wire.Response
		resp.Code = 404

		resp.Headers = append(resp.Headers, &wire.Header{
			Name:  "X-FailureReason",
			Value: []string{fmt.Sprintf("no known edge service: %s", req.Host)},
		})

		_, err := fw.WriteMarshal(1, &resp)
		return err
	}

	return serv.Handler.HandleRequest(ctx, L, &frameAccessor{fr, fw}, req)

	/*
		hreq, err := http.NewRequestWithContext(ctx, req.Method, h.localUrl+req.Path, fr.ReadAdapter())
		if err != nil {
			return err
		}
		hreq.URL.RawQuery = req.Query
		hreq.URL.Fragment = req.Fragment
		if req.Auth != nil {
			hreq.URL.User = url.UserPassword(req.Auth.User, req.Auth.Password)
		}

		hresp, err := http.DefaultClient.Do(hreq)
		if err != nil {
			return err
		}

		defer hresp.Body.Close()

		var resp wire.Response
		resp.Code = int32(hresp.StatusCode)

		for k, v := range hresp.Header {
			resp.Headers = append(resp.Headers, &wire.Header{
				Name:  k,
				Value: v,
			})
		}

		_, err = fw.WriteMarshal(1, &resp)
		if err != nil {
			return err
		}

		n, _ := io.Copy(stream, hresp.Body)

		L.Info("request ended", "size", n)

		return nil
	*/
}
