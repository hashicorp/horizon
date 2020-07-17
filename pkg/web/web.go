package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	lru "github.com/hashicorp/golang-lru"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/timing"
	"github.com/hashicorp/horizon/pkg/wire"
	servertiming "github.com/mitchellh/go-server-timing"
	"golang.org/x/time/rate"
)

type HostnameChecker interface {
	HandlingHostname(name string) bool
}

type Connector interface {
	ConnectToService(
		ctx context.Context,
		target *pb.ServiceRoute,
		account *pb.Account,
		proto string,
		token string,
	) (wire.Context, error)
}

type ratesPerAccount struct {
	bandwidth *rate.Limiter
	requests  *rate.Limiter

	clampValue int

	warn *int64
}

type Frontend struct {
	L       hclog.Logger
	client  *control.Client
	hub     Connector
	Checker HostnameChecker
	token   string

	mu    sync.Mutex
	rates *lru.ARCCache
}

func NewFrontend(L hclog.Logger, h Connector, cl *control.Client, token string) (*Frontend, error) {
	lr, err := lru.NewARC(10000)
	if err != nil {
		return nil, err
	}

	return &Frontend{
		L:      L,
		client: cl,
		hub:    h,
		token:  token,
		rates:  lr,
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
	// Add rate limiting here.
	var th servertiming.Header

	var tr timing.DefaultTracker

	ctx := timing.WithTracker(req.Context(), &tr)

	start := time.Now()

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

	rm := th.NewMetric("resolve").Start()

	account, target, limits, err := f.client.ResolveLabelLink(ll)
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

		account, target, limits, err = f.client.ResolveLabelLink(ll)
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

	// we should always have limits, but in the case that something is using an old API and
	// we see this as nil, just use an empty value.
	if limits == nil {
		limits = &pb.Account_Limits{}
	}

	rm.Stop()

	var rates *ratesPerAccount

	rv, ok := f.rates.Get(account.SpecString())
	if ok {
		rates = rv.(*ratesPerAccount)
	} else {
		bwLimit := rate.Limit(limits.Bandwidth)

		if limits.Bandwidth < 0.00001 {
			bwLimit = rate.Inf
		}

		reqLimit := rate.Limit(limits.HttpRequests)

		if limits.HttpRequests < 0.00001 {
			reqLimit = rate.Inf
		}

		rates = &ratesPerAccount{
			bandwidth:  rate.NewLimiter(bwLimit, int(limits.Bandwidth/10)),
			requests:   rate.NewLimiter(reqLimit, 1),
			clampValue: int(limits.Bandwidth / 10),
			warn:       new(int64),
		}

		f.rates.Add(account.SpecString(), rates)
	}

	if !rates.requests.Allow() {
		res := rates.requests.Reserve()
		defer res.Cancel()

		f.L.Info("request limit hit", "target", target.SpecString(), "account", account.SpecString())

		http.Error(
			w,
			fmt.Sprintf("request exceeded configured account. Time til next opening: %s", res.Delay()),
			429,
		)

		return
	}

	if atomic.LoadInt64(rates.warn) != 0 {
		res := rates.bandwidth.Reserve()
		if res.OK() {
			atomic.StoreInt64(rates.warn, 0)
		}

		res.Cancel()
	}

	lu := th.NewMetric("lookup").Start()

	reqId := pb.NewULID()

	f.L.Info("request",
		"id", reqId,
		"target", req.Host,
		"method", req.Method,
		"path", req.URL.Path,
		"content-length", req.ContentLength,
	)

	defer func() {
		f.L.Info("request finished", "id", reqId, "duration", time.Since(start))
	}()

	services, err := f.client.LookupService(ctx, account, target)
	if err != nil {
		f.L.Error("error resolving labels to services", "error", err, "labels", target)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(services) == 0 {
		f.L.Error("no deployments for service",
			"account", account,
			"target", target,
		)
		http.Error(w, "no deployments for service", http.StatusNotFound)
		return
	}

	lu.Stop()

	var wctx wire.Context

	for _, rs := range services {
		if rs.Type != "http" {
			f.L.Warn("service was not type http", "service-id", rs.Id, "type", rs.Type)
			continue
		}

		wctx, err = f.hub.ConnectToService(ctx, rs, account, "http", f.token)
		if err == nil {
			break
		}

		f.L.Warn("error connecting to service", "error", err, "labels", target, "service", rs.Id, "hub", rs.Hub)
		continue
	}

	if wctx == nil {
		f.L.Error("no viable service found", "labels", target, "candidates", len(services))
		http.Error(w, "unable to find viable endpoint", http.StatusInternalServerError)
		return
	}

	bt := th.NewMetric("request").Start()

	defer wctx.Close()

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

	err = wctx.WriteMarshal(1, &wreq)
	if err != nil {
		f.L.Error("error connecting to service", "error", err, "labels", target)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	adapter := wctx.Writer()
	io.Copy(adapter, req.Body)
	adapter.Close()

	bt.Stop()

	rt := th.NewMetric("response-header").Start()

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

	rt.Stop()

	for _, span := range tr.Spans() {
		th.Add(&servertiming.Metric{
			Name:     span.Name,
			Duration: span.Duration,
		})
	}

	hdr.Add("X-Horizon-Latency", time.Since(start).String())
	hdr.Add(servertiming.HeaderKey, th.String())

	if atomic.LoadInt64(rates.warn) != 0 {
		hdr.Add("X-Horizon-Warn", "This account is experiencing rate limiting.")
	}

	w.WriteHeader(int(wresp.Code))

	f.L.Trace("copying request body", "id", reqId)
	io.Copy(w, &ratedReader{f: f, r: wctx.Reader(), acc: rates})
}

type ratedReader struct {
	f   *Frontend
	r   io.Reader
	acc *ratesPerAccount
}

func (r *ratedReader) Read(b []byte) (int, error) {
	if r.acc.clampValue > 0 {
		if len(b) > r.acc.clampValue {
			b = b[:r.acc.clampValue]
		}
	}

	n, err := r.r.Read(b)
	if err != nil {
		return n, err
	}

	tokens := n / 1024
	if tokens == 0 {
		tokens = 1
	}

	res := r.acc.bandwidth.ReserveN(time.Now(), tokens)
	defer res.Cancel()

	if res.OK() {
		return n, nil
	}

	if atomic.CompareAndSwapInt64(r.acc.warn, 0, 1) {
		r.f.L.Debug("introducing delay to manage bandwidth usage", "delay", res.Delay())
	}

	time.Sleep(res.Delay())

	return n, nil
}
