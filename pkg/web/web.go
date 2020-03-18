package web

import (
	"io"
	"net"
	"net/http"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
)

type Performer interface {
	PerformRequest(req *wire.Request, body io.Reader, target string) (*wire.Response, io.Reader, error)
}

type Frontend struct {
	L         hclog.Logger
	Performer Performer
}

func (f *Frontend) Serve(l net.Listener) error {
	return http.Serve(l, f)
}

func (f *Frontend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var wreq wire.Request
	wreq.Method = req.Method
	wreq.Path = req.URL.RawPath

	for k, v := range req.Header {
		wreq.Headers = append(wreq.Headers, &wire.Header{
			Name:  k,
			Value: v,
		})
	}

	target := req.Host

	f.L.Info("request",
		"target", target,
		"method", req.Method,
		"path", req.URL.RawPath,
		"content-length", req.ContentLength,
	)

	wresp, body, err := f.Performer.PerformRequest(&wreq, req.Body, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	hdr := w.Header()

	for _, h := range wresp.Headers {
		for _, v := range h.Value {
			hdr.Add(h.Name, v)
		}
	}

	w.WriteHeader(int(wresp.Code))

	io.Copy(w, body)
}
