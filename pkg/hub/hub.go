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
}

func NewHub(L hclog.Logger, client *control.Client) (*Hub, error) {
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
		active: make(map[string]*agentConnection),
		cc:     client,
		id:     client.Id(),
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

func (hub *Hub) Run(ctx context.Context, li net.Listener) error {
	npn := map[string]control.NPNHandler{
		"hzn": hub.handleHZN,
	}

	err := hub.cc.RunIngress(ctx, li, npn)
	if err != nil {
		if no, ok := err.(*net.OpError); ok {
			if no.Err.Error() == "use of closed network connection" {
				return nil
			}
		}

		return err
	}

	return nil
}

func (hub *Hub) WaitToDrain() error {
	hub.wg.Wait()

	return nil
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
	ID        *pb.ULID
	AccountId *pb.ULID
	Start     *pb.Timestamp
	End       *pb.Timestamp
	Services  int32

	ActiveStreams *int64
	TotalStreams  *int64
}

func (h *Hub) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	fr, err := wire.NewFramingReader(conn)
	if err != nil {
		h.L.Error("error creating frame reader", "error", err)
		return
	}

	defer fr.Recycle()

	var preamble pb.Preamble

	tag, _, err := fr.ReadMarshal(&preamble)
	if err != nil {
		h.L.Error("error decoding preamble", "error", err)
		return
	}

	if tag != 1 {
		h.L.Error("protocol error detected in preamble", "tag", tag)
		return
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

	fw, err := wire.NewFramingWriter(conn)
	if err != nil {
		h.L.Error("error creating frame writer", "error", err)
		return
	}

	fw.Recycle()

	vt, err := h.ValidateToken(preamble.Token)
	if err != nil {
		h.L.Error("invalid token recieved", "error", err)
		wc.Status = "bad-token"

		_, err = fw.WriteMarshal(1, &wc)
		if err != nil {
			h.L.Error("error marshaling confirmation", "error", err)
		}

		return
	}

	if len(preamble.Services) > 0 {
		ok, _ := vt.HasCapability("hzn:serve")
		if !ok {
			wc.Status = "bad-token-capability"

			_, err = fw.WriteMarshal(1, &wc)
			if err != nil {
				h.L.Error("error marshaling confirmation", "error", err)
			}

			return
		}
	}

	for _, serv := range preamble.Services {
		err = h.cc.AddService(ctx, &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: vt.AccountNamespace(),
				AccountId: vt.AccountId(),
			},
			Hub:      h.id,
			Id:       serv.ServiceId,
			Type:     serv.Type,
			Labels:   serv.Labels,
			Metadata: serv.Metadata,
		})

		if err != nil {
			h.L.Error("error adding services", "error", err)
			return
		}
	}

	defer func() {
		for _, serv := range preamble.Services {
			err = h.cc.RemoveService(ctx, &pb.ServiceRequest{
				Account: &pb.Account{
					Namespace: vt.AccountNamespace(),
					AccountId: vt.AccountId(),
				},
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
	}()

	_, err = fw.WriteMarshal(1, &wc)
	if err != nil {
		h.L.Error("error marshaling confirmation", "error", err)
		return
	}

	fw.Recycle()

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

	h.mu.Lock()
	for _, serv := range preamble.Services {
		h.active[serv.ServiceId.SpecString()] = &agentConnection{
			useLZ4:  useLZ4,
			session: sess,
		}
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, serv := range preamble.Services {
			delete(h.active, serv.ServiceId.SpecString())
		}
	}()

	ai := &agentConn{
		ID:            pb.NewULID(),
		AccountId:     vt.AccountId(),
		Services:      int32(len(preamble.Services)),
		ActiveStreams: new(int64),
		TotalStreams:  new(int64),
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

		atomic.AddInt64(ai.ActiveStreams, 1)
		atomic.AddInt64(ai.TotalStreams, 1)

		h.sendAgentInfoFlow(ai)

		h.L.Trace("stream accepted", "id", stream.StreamID(), "lz4", useLZ4)

		var (
			r io.Reader = stream
			w io.Writer = stream
		)

		if useLZ4 {
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

		wctx := wire.NewContext(vt.AccountId(), fr, fw)

		h.L.Trace("accepted yamux session", "id", stream.StreamID())

		go h.handleAgentStream(ctx, ai, vt, stream, wctx)
	}
}
