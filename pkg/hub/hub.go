package hub

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
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

	reg Registry

	mu     sync.RWMutex
	active map[string]*yamux.Session
}

func NewHub(L hclog.Logger, r Registry) (*Hub, error) {
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

	fr := &wire.Framing{RW: conn}

	var preamble wire.Preamble

	_, err := fr.ReadMessage(&preamble)
	if err != nil {
		h.L.Error("error decoding preamble", "error", err)
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

	_, err = fr.WriteMessage(&wc)
	if err != nil {
		h.L.Error("error marshaling confirmation", "error", err)
		return
	}

	sess, err := yamux.Server(conn, h.cfg)
	if err != nil {
		h.L.Error("error configuring yamux session", "error", err)
		return
	}

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

type frameWriter struct {
	buf [2]byte
	w   io.Writer
}

func (f *frameWriter) Write(b []byte) (int, error) {
	var total int

	for len(b) > math.MaxUint16 {
		chunk := b[:math.MaxUint16]

		binary.BigEndian.PutUint16(f.buf[:], uint16(len(chunk)))
		n, err := f.w.Write(chunk)
		if err != nil {
			return 0, err
		}

		total += n

		b = b[math.MaxUint16:]
	}

	binary.BigEndian.PutUint16(f.buf[:], uint16(len(b)))
	n, err := f.w.Write(f.buf[:])
	if err != nil {
		return total, err
	}

	total += n

	n, err = f.w.Write(b)
	if err != nil {
		return total, err
	}

	total += n

	return total, nil
}

func (f *frameWriter) Close() error {
	binary.BigEndian.PutUint16(f.buf[:], 0)
	_, err := f.w.Write(f.buf[:])
	return err
}

func (h *Hub) PerformRequest(req *wire.Request, body io.Reader, target string) (*wire.Response, io.Reader, error) {
	session, err := h.findSession(target)
	if err != nil {
		return nil, nil, err
	}

	stream, err := session.OpenStream()
	if err != nil {
		return nil, nil, err
	}

	fr := &wire.Framing{RW: stream}

	_, err = fr.WriteMessage(req)
	if err != nil {
		return nil, nil, err
	}

	fw := &frameWriter{w: stream}
	io.Copy(fw, body)
	fw.Close()

	var resp wire.Response

	_, err = fr.ReadMessage(&resp)
	if err != nil {
		return nil, nil, err
	}

	return &resp, stream, nil
}
