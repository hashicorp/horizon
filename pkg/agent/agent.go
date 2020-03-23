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
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/oklog/ulid"
)

type Agent struct {
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
		cfg:      cfg,
		localUrl: "http://" + addr,
		key:      key,
	}, nil
}

var ErrProtocolError = errors.New("protocol error detected")

func (a *Agent) Nego(ctx context.Context, L hclog.Logger, conn net.Conn, peer string) error {
	nconn, err := noiseconn.NewConn(conn)
	if err != nil {
		return err
	}

	err = nconn.Connect(a.key, peer)
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

	go a.watchSession(ctx, L, session, fr)

	return nil
}

func (a *Agent) watchSession(ctx context.Context, L hclog.Logger, session *yamux.Session, fr *wire.FramingReader) {
	defer fr.Recycle()

	go func() {
		defer session.Close()

		select {
		case <-ctx.Done():
			// ok
		case <-session.CloseChan():
			// ok
		}
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			L.Warn("error accepting yamux stream", "error", err)
			return
		}

		go a.handleStream(ctx, L, stream)
	}
}

func (a *Agent) handleStream(ctx context.Context, L hclog.Logger, stream *yamux.Stream) {
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
		err = a.handleHTTP(ctx, L, stream, fr, fw, &req)
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

func (a *Agent) handleHTTP(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.FramingReader, fw *wire.FramingWriter, req *wire.Request) error {
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

	return nil
}
