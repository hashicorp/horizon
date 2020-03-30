package hub

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/edgeservices"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/registry"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/pkg/errors"
)

type Registry interface {
	AuthAgent(L hclog.Logger, token, pubKey string, labels []string, services []*wire.ServiceInfo) (string, func(), error)
	ResolveAgent(L hclog.Logger, target string) (registry.ResolvedService, error)
}

type Hub struct {
	L   hclog.Logger
	cfg *yamux.Config
	key noise.DHKey

	reg *registry.Registry

	services edgeservices.Services

	mu     sync.RWMutex
	active map[string]*yamux.Session
}

func NewHub(L hclog.Logger, r *registry.Registry, key noise.DHKey) (*Hub, error) {
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

	agentKey, headers, remove, err := h.reg.AuthAgent(h.L, preamble.Token, nconn.PeerStatic(), preamble.Labels, preamble.Services)
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
			if err == io.EOF {
				h.L.Info("agent disconnected", "session", preamble.SessionId)
			} else {
				h.L.Error("error accepting new yamux session", "error", err)
			}

			return
		}

		h.L.Trace("stream accepted", "id", stream.StreamID())

		fr, err := wire.NewFramingReader(stream)
		if err != nil {
			h.L.Error("error creating frame reader", "error", err)
			continue
		}

		defer fr.Recycle()

		fw, err := wire.NewFramingWriter(stream)
		if err != nil {
			h.L.Error("error creating framing writer", "error", err)
			continue
		}

		defer fw.Recycle()

		wctx := wire.NewContext(headers.AccountId().String(), fr, fw)

		h.L.Trace("accepted yamux session", "id", stream.StreamID())

		go h.handleAgentStream(ctx, stream, wctx)
	}
}

func (h *Hub) findSession(target string) (*yamux.Session, registry.ResolvedService, error) {
	rs, err := h.reg.ResolveAgent(h.L, target)
	if err != nil {
		return nil, rs, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	sess, ok := h.active[rs.Agent]
	if !ok {
		return nil, rs, io.EOF
	}

	return sess, rs, nil
}

var (
	ErrProtocolError = errors.New("protocol error")
	ErrWrongService  = errors.New("wrong service")
)

func (h *Hub) PerformRequest(req *wire.Request, body io.Reader, target, serviceType string) (*wire.Response, io.Reader, error) {
	session, rs, err := h.findSession(target)
	if err != nil {
		return nil, nil, err
	}

	if rs.ServiceType != serviceType {
		return nil, nil, errors.Wrapf(ErrWrongService, "incorrect service type (%s != %s)", serviceType, rs.ServiceType)
	}

	req.TargetService = rs.ServiceId

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
