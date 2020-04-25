package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/y0ssar1an/q"
)

type HostnameChecker interface {
	HandlingHostname(name string) bool
}

/*
type LabelResolver interface {
	FindLabelLink(labels []string) (string, []string, error)
	MatchServices(accid string, labels []string) ([]registry.ResolvedService, error)
}

type Connector interface {
	ConnectToService(req *wire.Request, accid string, rs registry.ResolvedService) (wire.Context, error)
}
*/

type Frontend struct {
	L      hclog.Logger
	client *control.Client
	hub    *hub.Hub
	// LabelResolver LabelResolver
	// Connector     Connector
	Checker HostnameChecker
}

func NewFrontend(L hclog.Logger, h *hub.Hub, cl *control.Client) (*Frontend, error) {
	return &Frontend{
		L:      L,
		client: cl,
		hub:    h,
	}, nil
}

func (f *Frontend) Serve(l net.Listener) error {
	return http.Serve(l, f)
}

func (f *Frontend) extractPrefixHost(host string) (string, string, bool) {
	var first, domain string

	firstDot := strings.IndexByte(host, '.')
	if firstDot != -1 {
		first = host[:firstDot]
		domain = host[firstDot:]
	} else {
		first = host
		domain = ""
	}

	lastDash := strings.LastIndexByte(first, '-')
	if lastDash == -1 {
		return "", "", false
	}

	return first[:lastDash+1] + domain, first[lastDash+1:], true
}

func (f *Frontend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	ll := &pb.LabelSet{
		Labels: []*pb.Label{
			{
				Name:  ":hostname",
				Value: req.Host,
			},
		},
	}

	var (
		prefixHost, deployId string
		usingPrefix          bool
	)

	accountId, target, err := f.client.ResolveLabelLink(ll)
	if err != nil || target == nil {
		prefixHost, deployId, usingPrefix = f.extractPrefixHost(req.Host)
		if !usingPrefix {
			f.L.Error("unable to resolve label link", "error", err, "hostname", req.Host)
			http.Error(w, fmt.Sprintf("no registered application for hostname: %s", req.Host), http.StatusInternalServerError)
			return
		}

		ll.Labels = []*pb.Label{
			{
				Name:  ":hostname",
				Value: prefixHost,
			},
		}

		accountId, target, err = f.client.ResolveLabelLink(ll)
		if err != nil || target == nil {
			f.L.Error("unable to resolve label link", "error", err, "hostname", req.Host)
			http.Error(w, fmt.Sprintf("no registered application for hostname: %s", req.Host), http.StatusInternalServerError)
			return
		}

		target.Labels = append(target.Labels, &pb.Label{
			Name:  ":deployment",
			Value: deployId,
		})

		target.Finalize()
	}

	f.L.Info("request",
		"target", req.Host,
		"method", req.Method,
		"path", req.URL.Path,
		"content-length", req.ContentLength,
	)

	q.Q(target)

	services, err := f.client.LookupService(ctx, accountId, target)
	if err != nil {
		f.L.Error("error resolving labels to services", "error", err, "labels", target)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(services) == 0 {
		http.Error(w, "no deployments for service", http.StatusNotFound)
		return
	}

	rs := services[0]

	if rs.Type != "http" {
		f.L.Error("service was not type http", "type", rs.Type)
		http.Error(w, "no http services available", http.StatusNotFound)
		return
	}

	var wreq pb.Request
	wreq.Host = req.Host
	wreq.Method = req.Method
	wreq.Path = req.URL.EscapedPath()
	wreq.Query = req.URL.RawQuery
	wreq.Fragment = req.URL.Fragment
	if user, pass, ok := req.BasicAuth(); ok {
		wreq.Auth = &pb.Auth{
			User:     user,
			Password: pass,
		}
	}

	for k, v := range req.Header {
		wreq.Headers = append(wreq.Headers, &pb.Header{
			Name:  k,
			Value: v,
		})
	}

	wctx, err := f.hub.ConnectToService(ctx, rs, accountId, "http")
	if err != nil {
		f.L.Error("error connecting to service", "error", err, "labels", target)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = wctx.WriteMarshal(1, &wreq)
	if err != nil {
		f.L.Error("error connecting to service", "error", err, "labels", target)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	adapter := wctx.Writer()
	io.Copy(adapter, req.Body)
	adapter.Close()

	var wresp pb.Response

	tag, err := wctx.ReadMarshal(&wresp)
	if err != nil || tag != 1 {
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

	io.Copy(w, wctx.Reader())
}
