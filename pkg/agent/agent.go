package agent

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/oklog/ulid"
)

type Agent struct {
	cfg      *yamux.Config
	localUrl string

	LocalAddr string
	Token     string
	Labels    []string
}

func NewAgent(L hclog.Logger, addr string) (*Agent, error) {
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
	}, nil
}

func (a *Agent) Nego(ctx context.Context, L hclog.Logger, conn net.Conn) error {
	id, err := ulid.New(ulid.Now(), ulid.Monotonic(rand.Reader, 1))
	if err != nil {
		return err
	}

	fr := &wire.Framing{RW: conn}

	var preamble wire.Preamble
	preamble.Token = a.Token
	preamble.SessionId = id.String()
	preamble.Labels = a.Labels

	_, err = fr.WriteMessage(&preamble)
	if err != nil {
		return err
	}

	t := time.Now()

	var wc wire.Confirmation
	_, err = fr.ReadMessage(&wc)
	if err != nil {
		return err
	}

	latency := time.Since(t)

	L.Debug("connection latency", "latency", latency)

	st := time.Unix(int64(wc.Time.Sec), int64(wc.Time.Nsec))

	skew := time.Since(st)

	session, err := yamux.Client(conn, a.cfg)
	if err != nil {
		return err
	}

	L.Info("connected successfully", "status", wc.Status, "latency", latency, "skew", skew)

	go a.watchSession(ctx, L, session)

	return nil
}

func (a *Agent) watchSession(ctx context.Context, L hclog.Logger, session *yamux.Session) {
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

	fr := &wire.Framing{RW: stream}

	var req wire.Request

	_, err := fr.ReadMessage(&req)
	if err != nil {
		L.Error("error decoding request", "error", err)
		return
	}

	switch req.Type {
	case wire.HTTP:
		err = a.handleHTTP(ctx, L, stream, fr, &req)
		if err != nil {
			L.Error("error processing http request", "error", err)
		}
	default:
		L.Error("unknown request type detected", "type", req.Type)

		var resp wire.Response
		resp.Error = "unknown type"

		_, err = fr.WriteMessage(&resp)
		if err != nil {
			L.Error("error marshaling response", "error", err)
			return
		}
	}
}

type frameReader struct {
	buf    [2]byte
	r      io.Reader
	rest   int
	closed bool
}

func (f *frameReader) Read(b []byte) (int, error) {
	if f.closed {
		return 0, io.EOF
	}

	if f.rest > 0 {
		if f.rest < len(b) {
			n := f.rest
			f.rest = 0
			return io.ReadFull(f.r, b[:n])
		} else {
			n := len(b)
			f.rest -= n
			return io.ReadFull(f.r, b)
		}
	}

	_, err := io.ReadFull(f.r, f.buf[:])
	if err != nil {
		return 0, err
	}

	sz := int(binary.BigEndian.Uint16(f.buf[:]))

	if sz == 0 {
		f.closed = true
		return 0, io.EOF
	}

	if sz < len(b) {
		return io.ReadFull(f.r, b[:sz])
	}

	n, err := io.ReadFull(f.r, b)
	if err != nil {
		return n, err
	}

	f.rest = sz - n

	return n, nil
}

func (a *Agent) handleHTTP(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.Framing, req *wire.Request) error {
	L.Info("request started", "method", req.Method, "path", req.Path)

	hreq, err := http.NewRequestWithContext(ctx, req.Method, a.localUrl+req.Path, &frameReader{r: stream})
	if err != nil {
		return err
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

	_, err = fr.WriteMessage(&resp)
	if err != nil {
		return err
	}

	n, _ := io.Copy(stream, hresp.Body)

	L.Info("request ended", "size", n)

	return nil
}
