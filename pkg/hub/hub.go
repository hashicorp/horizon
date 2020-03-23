package hub

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

type Registry interface {
	AuthAgent(L hclog.Logger, token string, labels []string) (string, func(), error)
	ResolveAgent(L hclog.Logger, target string) (string, error)
}

type Hub struct {
	L   hclog.Logger
	cfg *yamux.Config
	key noise.DHKey

	reg Registry

	mu     sync.RWMutex
	active map[string]*yamux.Session
}

func NewHub(L hclog.Logger, r Registry, key noise.DHKey) (*Hub, error) {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})
	cfg.LogOutput = nil

	h := &Hub{
		L:      L,
		cfg:    cfg,
		reg:    r,
		active: make(map[string]*yamux.Session),
		key:    key,
	}

	return h, nil
}

func (h *Hub) Serve(ctx context.Context, l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}

		go h.handleConn(ctx, conn)
	}
}

func (h *Hub) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	nconn, err := noiseconn.NewConn(conn)
	if err != nil {
		h.L.Error("error creating noise conn", "error", err)
		return
	}

	err = nconn.Accept(h.key)
	if err != nil {
		h.L.Error("error initializing noise conn", "error", err)
		return
	}

	fr, err := wire.NewFramingReader(nconn)
	if err != nil {
		h.L.Error("error creating frame reader", "error", err)
		return
	}

	defer fr.Recycle()

	var preamble wire.Preamble

	tag, _, err := fr.ReadMarshal(&preamble)
	if err != nil {
		h.L.Error("error decoding preamble", "error", err)
		return
	}

	if tag != 1 {
		h.L.Error("protocol error detected in preamble", "tag", tag)
		return
	}

	agentKey, remove, err := h.reg.AuthAgent(h.L, preamble.Token, preamble.Labels)
	if err != nil {
		h.L.Error("error authenticating agent", "error", err)
		return
	}

	defer remove()

	ts := time.Now()

	var wc wire.Confirmation
	wc.Time = &wire.Timestamp{
		Sec:  uint64(ts.Unix()),
		Nsec: uint64(ts.Nanosecond()),
	}

	wc.Status = "connected"

	fw, err := wire.NewFramingWriter(nconn)
	if err != nil {
		h.L.Error("error creating frame writer", "error", err)
		return
	}

	_, err = fw.WriteMarshal(1, &wc)
	if err != nil {
		h.L.Error("error marshaling confirmation", "error", err)
		return
	}

	fw.Recycle()

	bc := &wire.ComposedConn{
		Reader: fr.BufReader(),
		Writer: nconn,
		Closer: conn,
	}

	sess, err := yamux.Server(bc, h.cfg)
	if err != nil {
		h.L.Error("error configuring yamux session", "error", err)
		return
	}

	defer sess.Close()

	h.mu.Lock()
	h.active[agentKey] = sess
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.active, agentKey)
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			h.L.Error("error accepting new yamux session", "error", err)
			return
		}

		h.L.Trace("accepted yamux session", "id", stream.StreamID())
	}
}

func (h *Hub) findSession(target string) (*yamux.Session, error) {
	agentKey, err := h.reg.ResolveAgent(h.L, target)
	if err != nil {
		return nil, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	sess, ok := h.active[agentKey]
	if !ok {
		return nil, io.EOF
	}

	return sess, nil
}

var ErrProtocolError = errors.New("protocol error")

func (h *Hub) PerformRequest(req *wire.Request, body io.Reader, target string) (*wire.Response, io.Reader, error) {
	session, err := h.findSession(target)
	if err != nil {
		return nil, nil, err
	}

	stream, err := session.OpenStream()
	if err != nil {
		return nil, nil, err
	}

	fw, err := wire.NewFramingWriter(stream)
	if err != nil {
		return nil, nil, err
	}

	defer fw.Recycle()

	_, err = fw.WriteMarshal(1, req)
	if err != nil {
		return nil, nil, err
	}

	adapter := fw.WriteAdapter()
	io.Copy(adapter, body)
	adapter.Close()

	fr, err := wire.NewFramingReader(stream)
	if err != nil {
		return nil, nil, err
	}

	var resp wire.Response

	tag, _, err := fr.ReadMarshal(&resp)
	if err != nil {
		return nil, nil, err
	}

	if tag != 1 {
		return nil, nil, ErrProtocolError
	}

	return &resp, fr.RecylableBufReader(), nil
}
