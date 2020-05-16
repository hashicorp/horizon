package hub

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/horizon/pkg/web"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/pierrec/lz4"
	"github.com/pkg/errors"
)

var (
	ErrProtocolError = errors.New("protocol error")
	ErrWrongService  = errors.New("wrong service")
)

type agentConnection struct {
	useLZ4  bool
	session *yamux.Session
}

type Hub struct {
	L   hclog.Logger
	cfg *yamux.Config

	id *pb.ULID
	cc *control.Client

	// services edgeservices.Services

	mu     sync.RWMutex
	active map[string]*agentConnection

	// ServiceSorter ServiceSorter
	wg sync.WaitGroup

	location []*pb.NetworkLocation

	mux *http.ServeMux
	fe  *web.Frontend

	activeAgents *int64
	totalAgents  *int64
}

func NewHub(L hclog.Logger, client *control.Client, feToken string) (*Hub, error) {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})
	cfg.LogOutput = nil

	h := &Hub{
		L:            L,
		cfg:          cfg,
		active:       make(map[string]*agentConnection),
		cc:           client,
		id:           client.Id(),
		mux:          http.NewServeMux(),
		activeAgents: new(int64),
		totalAgents:  new(int64),
	}

	fe, err := web.NewFrontend(L, h, client, feToken)
	if err != nil {
		return nil, err
	}

	h.fe = fe
	h.mux.HandleFunc("/__hzn/healthz", h.handleHeathz)
	h.mux.Handle("/", h.fe)

	h.location = client.Locations()

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

func (hub *Hub) Run(ctx context.Context, li net.Listener) error {
	npn := map[string]control.NPNHandler{
		"hzn": hub.handleHZN,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go hub.sendStats(ctx)

	err := hub.cc.RunIngress(ctx, li, npn, hub)
	if err != nil {
		if no, ok := err.(*net.OpError); ok {
			if no.Err.Error() == "use of closed network connection" {
				return nil
			}
		}

		if err == http.ErrServerClosed {
			return nil
		}

		return err
	}

	return nil
}

func (hub *Hub) sendStats(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			active := atomic.LoadInt64(hub.activeAgents)
			hub.cc.SendFlow(&pb.FlowRecord{
				HubStats: &pb.FlowRecord_HubStats{
					HubId:        hub.cc.StableId(),
					ActiveAgents: active,
					TotalAgents:  atomic.LoadInt64(hub.totalAgents),
				},
			})

			hub.L.Trace("hub stats", "active-agents", active)
		}
	}
}

func (hub *Hub) WaitToDrain() error {
	hub.wg.Wait()

	return nil
}

func (hub *Hub) ListenHTTP(addr string) error {
	return http.ListenAndServe(addr, hub)
}

func (hub *Hub) handleHZN(hs *http.Server, tlsConn *tls.Conn, h http.Handler) {
	// Use the same trick http2 does to extract a context.
	var ctx context.Context
	type baseContexter interface {
		BaseContext() context.Context
	}

	if bc, ok := h.(baseContexter); ok {
		ctx = bc.BaseContext()
	}

	hub.wg.Add(1)
	defer hub.wg.Done()

	hub.handleConn(ctx, tlsConn)
}

func (h *Hub) ValidateToken(stoken string) (*token.ValidToken, error) {
	return token.CheckTokenED25519(stoken, h.cc.TokenPub())
}

type agentConn struct {
	ID       *pb.ULID
	Account  *pb.Account
	Start    *pb.Timestamp
	End      *pb.Timestamp
	Services int32

	ActiveStreams *int64
	TotalStreams  *int64

	stoken   string
	preamble *pb.Preamble

	token *token.ValidToken

	sess        *yamux.Session
	useLZ4      bool
	cleanups    []func()
	connectOnly bool
}

func (ai *agentConn) cleanup() {
	for _, f := range ai.cleanups {
		f()
	}
}

func (h *Hub) handshake(ctx context.Context, fr *wire.FramingReader, fw *wire.FramingWriter) (*agentConn, error) {
	var preamble pb.Preamble

	tag, _, err := fr.ReadMarshal(&preamble)
	if err != nil {
		return nil, errors.Wrapf(err, "error decoding preamble")
	}

	if tag != 1 {
		return nil, errors.Wrapf(err, "protocol error detected in preamble - wrong tag")
	}

	ts := time.Now()

	var wc pb.Confirmation
	wc.Time = &pb.Timestamp{
		Sec:  uint64(ts.Unix()),
		Nsec: uint64(ts.Nanosecond()),
	}

	wc.Status = "connected"

	var useLZ4 bool

	if preamble.Compression == "lz4" {
		wc.Compression = "lz4"
		useLZ4 = true
	}

	vt, err := h.ValidateToken(preamble.Token)
	if err != nil {
		h.L.Error("invalid token received", "error", err)
		wc.Status = "bad-token"

		_, err = fw.WriteMarshal(1, &wc)
		if err != nil {
			return nil, errors.Wrapf(err, "error marshalling confirmation")
		}

		return nil, errors.Wrapf(err, "invalid token received")
	}

	if len(preamble.Services) > 0 {
		ok, _ := vt.HasCapability(pb.SERVE)
		if !ok {
			wc.Status = "bad-token-capability"

			_, err = fw.WriteMarshal(1, &wc)
			if err != nil {
				return nil, errors.Wrapf(err, "error marshalling confirmation")
			}

			return nil, errors.Wrapf(ErrProtocolError, "token not authorized to serve")
		}
	}

	for _, serv := range preamble.Services {
		err = h.cc.AddService(ctx, &pb.ServiceRequest{
			Account:  vt.Account(),
			Hub:      h.id,
			Id:       serv.ServiceId,
			Type:     serv.Type,
			Labels:   serv.Labels,
			Metadata: serv.Metadata,
		})

		if err != nil {
			return nil, errors.Wrapf(err, "error adding services")
		}

		h.L.Debug("adding service",
			"hub", h.id,
			"service", serv.ServiceId,
			"labels", serv.Labels.SpecString(),
			"account", vt.Account(),
		)
	}

	_, err = fw.WriteMarshal(1, &wc)
	if err != nil {
		return nil, errors.Wrapf(err, "error marshalling confirmation")
	}

	cleanup := func() {
		for _, serv := range preamble.Services {
			err = h.cc.RemoveService(ctx, &pb.ServiceRequest{
				Account:  vt.Account(),
				Hub:      h.id,
				Id:       serv.ServiceId,
				Type:     serv.Type,
				Labels:   serv.Labels,
				Metadata: serv.Metadata,
			})

			if err != nil {
				h.L.Error("error removing service", "error", err)
				// we want to try all of them regardless of the error.
			}
		}
	}

	ai := &agentConn{
		ID:            pb.NewULID(),
		Account:       vt.Account(),
		Services:      int32(len(preamble.Services)),
		ActiveStreams: new(int64),
		TotalStreams:  new(int64),
		stoken:        preamble.Token,
		preamble:      &preamble,
		token:         vt,
		useLZ4:        useLZ4,
		cleanups:      []func(){cleanup},
		connectOnly:   len(preamble.Services) == 0,
	}

	return ai, nil
}

func (h *Hub) registerAgent(ai *agentConn) error {
	atomic.AddInt64(h.activeAgents, 1)
	atomic.AddInt64(h.totalAgents, 1)

	h.mu.Lock()
	for _, serv := range ai.preamble.Services {
		h.active[serv.ServiceId.SpecString()] = &agentConnection{
			useLZ4:  ai.useLZ4,
			session: ai.sess,
		}
	}
	h.mu.Unlock()

	h.L.Debug("register agent", "id", ai.ID)

	ai.cleanups = append(ai.cleanups, func() {
		h.L.Debug("unregister agent", "id", ai.ID)
		atomic.AddInt64(h.activeAgents, -1)

		h.mu.Lock()
		for _, serv := range ai.preamble.Services {
			delete(h.active, serv.ServiceId.SpecString())
		}
		h.mu.Unlock()
	})

	return nil
}

func (h *Hub) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	fr, err := wire.NewFramingReader(conn)
	if err != nil {
		h.L.Error("error creating frame reader", "error", err)
		return
	}

	defer fr.Recycle()

	fw, err := wire.NewFramingWriter(conn)
	if err != nil {
		h.L.Error("error creating frame writer", "error", err)
		return
	}

	defer fw.Recycle()

	ai, err := h.handshake(ctx, fr, fw)
	if err != nil {
		h.L.Error("error in agent handshake", "error", err)
		return
	}

	defer ai.cleanup()

	bc := &wire.ComposedConn{
		Reader: fr.BufReader(),
		Writer: conn,
		Closer: conn,
	}

	sess, err := yamux.Server(bc, h.cfg)
	if err != nil {
		h.L.Error("error configuring yamux session", "error", err)
		return
	}

	defer sess.Close()

	ai.sess = sess

	if !ai.connectOnly {
		err = h.registerAgent(ai)
		if err != nil {
			h.L.Error("error registering agent", "error", err)
		}
	}

	h.sendAgentInfoFlow(ai)
	defer func() {
		ai.End = pb.NewTimestamp(time.Now())
		h.sendAgentInfoFlow(ai)
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Send the agent flow every minute as a sort of heartbeat that the
	// agent is still connected.

	agentHB := time.NewTicker(time.Minute)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-agentHB.C:
				h.sendAgentInfoFlow(ai)
			}
		}
	}()

	h.L.Info("tracking new sessions for agent", "agent", ai.ID)

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			if err == io.EOF {
				h.L.Info("agent disconnected", "session", ai.ID)
			} else {
				h.L.Error("error accepting new yamux session", "error", err)
			}

			return
		}

		atomic.AddInt64(ai.ActiveStreams, 1)
		atomic.AddInt64(ai.TotalStreams, 1)

		h.sendAgentInfoFlow(ai)

		h.L.Trace("stream accepted", "id", stream.StreamID(), "lz4", ai.useLZ4)

		var (
			r io.Reader = stream
			w io.Writer = stream
		)

		if ai.useLZ4 {
			r = lz4.NewReader(stream)
			w = lz4.NewWriter(stream)
		}

		fr, err := wire.NewFramingReader(r)
		if err != nil {
			h.L.Error("error creating frame reader", "error", err)
			continue
		}

		defer fr.Recycle()

		fw, err := wire.NewFramingWriter(w)
		if err != nil {
			h.L.Error("error creating framing writer", "error", err)
			continue
		}

		defer fw.Recycle()

		wctx := wire.NewContext(ai.token.Account(), fr, fw)

		h.L.Trace("accepted yamux session", "id", stream.StreamID())

		go h.handleAgentStream(ctx, ai, stream, wctx)
	}
}
