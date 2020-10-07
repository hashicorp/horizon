package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
)

type httpHandler struct {
	url string
}

func HTTPHandler(url string) ServiceHandler {
	if !strings.HasPrefix(url, "http://") {
		url = "http://" + url
	}

	return &httpHandler{url}
}

func (h *httpHandler) HandleRequest(ctx context.Context, L hclog.Logger, sctx ServiceContext) error {
	proto := sctx.ProtocolId()

	if !(proto == "" || proto == "http") {
		return fmt.Errorf("unknown protocol: %s", proto)
	}

	var req pb.Request

	_, err := sctx.ReadMarshal(&req)
	if err != nil {
		return err
	}

	L.Info("request started", "method", req.Method, "path", req.Path)

	hreq, err := http.NewRequestWithContext(ctx, req.Method, h.url+req.Path, sctx.BodyReader())
	if err != nil {
		return err
	}

	hreq.Host = req.Host
	hreq.URL.RawQuery = req.Query
	hreq.URL.Fragment = req.Fragment
	if req.Auth != nil {
		hreq.URL.User = url.UserPassword(req.Auth.User, req.Auth.Password)
	}
	for _, h := range req.Headers {
		for _, v := range h.Value {
			hreq.Header.Add(h.Name, v)
		}
	}

	hresp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return err
	}

	defer hresp.Body.Close()

	var resp pb.Response
	resp.Code = int32(hresp.StatusCode)

	for k, v := range hresp.Header {
		resp.Headers = append(resp.Headers, &pb.Header{
			Name:  k,
			Value: v,
		})
	}

	err = sctx.WriteMarshal(1, &resp)
	if err != nil {
		return err
	}

	w := sctx.Writer()
	defer w.Close()

	n, _ := io.Copy(w, hresp.Body)

	L.Info("request ended", "size", n)

	return nil
}
