package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/oklog/ulid"
)

type Agent struct {
	L hclog.Logger

	cfg      *yamux.Config
	localUrl string
	key      noise.DHKey

	LocalAddr string
	Token     string
	Labels    []string
}

func NewAgent(L hclog.Logger, addr string, key noise.DHKey) (*Agent, error) {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})
	cfg.LogOutput = nil

	return &Agent{
		L:        L,
		cfg:      cfg,
		localUrl: "http://" + addr,
		key:      key,
	}, nil
}

type HubConfig struct {
	Addr      string
	PublicKey string
}

type hubStatus struct {
	cfg       HubConfig
	connected bool
	err       error
}

func (a *Agent) Run(ctx context.Context, initialHubs []HubConfig) error {
	status := make(chan hubStatus)

	for _, cfg := range initialHubs {
		go a.connectToHub(ctx, cfg, status)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case stat := <-status:
			if !stat.connected {
				a.L.Warn("connection to hub disconnected", "addr", stat.cfg.Addr)
				time.AfterFunc(10*time.Second, func() {
					go a.connectToHub(ctx, stat.cfg, status)
				})
			} else {
				a.L.Info("connected to hub", "addr", stat.cfg.Addr)
			}
		}
	}
}

func (a *Agent) connectToHub(ctx context.Context, hub HubConfig, status chan hubStatus) {
	a.L.Info("connecting to hub", "addr", hub.Addr)

	conn, err := net.Dial("tcp", hub.Addr)
	if err != nil {
		status <- hubStatus{cfg: hub, err: err}
		return
	}

	err = a.Nego(ctx, a.L, conn, hub, status)
	if err != nil {
		status <- hubStatus{cfg: hub, err: err}
		return
	}

	status <- hubStatus{cfg: hub, connected: true}
}

var ErrProtocolError = errors.New("protocol error detected")

func (a *Agent) Nego(ctx context.Context, L hclog.Logger, conn net.Conn, hubCfg HubConfig, status chan hubStatus) error {
	nconn, err := noiseconn.NewConn(conn)
	if err != nil {
		return err
	}

	err = nconn.Connect(a.key, hubCfg.PublicKey)
	if err != nil {
		return err
	}

	id, err := ulid.New(ulid.Now(), ulid.Monotonic(rand.Reader, 1))
	if err != nil {
		return err
	}

	fw, err := wire.NewFramingWriter(nconn)
	if err != nil {
		return err
	}

	defer fw.Recycle()

	fr, err := wire.NewFramingReader(nconn)
	if err != nil {
		return err
	}

	var preamble wire.Preamble
	preamble.Token = a.Token
	preamble.SessionId = id.String()
	preamble.Labels = a.Labels

	_, err = fw.WriteMarshal(1, &preamble)
	if err != nil {
		return err
	}

	t := time.Now()

	var wc wire.Confirmation
	tag, _, err := fr.ReadMarshal(&wc)
	if err != nil {
		return err
	}

	if tag != 1 {
		return ErrProtocolError
	}

	latency := time.Since(t)

	L.Debug("connection latency", "latency", latency)

	st := time.Unix(int64(wc.Time.Sec), int64(wc.Time.Nsec))

	skew := time.Since(st)

	bc := &wire.ComposedConn{
		Reader: fr.BufReader(),
		Writer: nconn,
		Closer: conn,
	}

	session, err := yamux.Client(bc, a.cfg)
	if err != nil {
		return err
	}

	L.Info("connected successfully", "status", wc.Status, "latency", latency, "skew", skew)

	go a.watchSession(ctx, L, session, fr, hubCfg, status)

	return nil
}

func (a *Agent) watchSession(ctx context.Context, L hclog.Logger, session *yamux.Session, fr *wire.FramingReader, hubCfg HubConfig, status chan hubStatus) {
	defer fr.Recycle()
	defer func() {
		status <- hubStatus{
			cfg: hubCfg,
		}
	}()

	defer session.Close()

	go func() {
		defer session.Close()

		select {
		case <-ctx.Done():
			// ok
		case <-session.CloseChan():
			// ok
		}
	}()

	var ltrans logTransmitter
	ltrans.path = "foo-bar"
	ltrans.session = session

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			L.Warn("error accepting yamux stream", "error", err)
			return
		}

		go a.handleStream(ctx, L, session, stream, &ltrans)
	}
}

func (a *Agent) handleStream(ctx context.Context, L hclog.Logger, session *yamux.Session, stream *yamux.Stream, ltrans *logTransmitter) {
	defer stream.Close()

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
		err = a.handleHTTP(ctx, L, stream, fr, fw, &req, ltrans)
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

func (a *Agent) handleHTTP(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.FramingReader, fw *wire.FramingWriter, req *wire.Request, ltrans *logTransmitter) error {
	L.Info("request started", "method", req.Method, "path", req.Path)

	hreq, err := http.NewRequestWithContext(ctx, req.Method, a.localUrl+req.Path, fr.ReadAdapter())
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

	var lm logs.Message
	lm.Timestamp = logs.Now()
	lm.Mesg = "performed request"
	lm.Attrs = []*logs.Attribute{
		{
			Key:  "method",
			Sval: req.Method,
		},
		{
			Key:  "path",
			Sval: req.Path,
		},
		{
			Key:  "response-code",
			Ival: int64(hresp.StatusCode),
		},
		{
			Key:  "body-size",
			Ival: int64(n),
		},
	}

	return ltrans.transmit(&lm)
}
