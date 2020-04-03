package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/oklog/ulid"
)

type ServiceHandler interface {
	HandleRequest(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.FramingReader, fw *wire.FramingWriter, req *wire.Request, ltrans *LogTransmitter) error
}

type Service struct {
	Id          wire.ULID
	Type        string
	Labels      []string
	Description string
	Handler     ServiceHandler
}

type Agent struct {
	L hclog.Logger

	cfg *yamux.Config
	key noise.DHKey

	LocalAddr string
	Token     string
	Labels    []string

	mu       sync.RWMutex
	services map[string]*Service
	sessions []*yamux.Session

	statuses chan hubStatus
}

func NewAgent(L hclog.Logger, key noise.DHKey) (*Agent, error) {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})
	cfg.LogOutput = nil

	agent := &Agent{
		L:        L,
		cfg:      cfg,
		key:      key,
		services: make(map[string]*Service),
		statuses: make(chan hubStatus),
	}

	return agent, nil
}

var mread = ulid.Monotonic(rand.Reader, 1)

func (a *Agent) AddService(serv *Service) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id, err := ulid.New(ulid.Now(), mread)
	if err != nil {
		return "", err
	}

	serv.Id.ULID = id

	a.services[serv.Id.String()] = serv
	return serv.Id.String(), nil
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
	err := a.Start(ctx, initialHubs)
	if err != nil {
		return err
	}

	return a.Wait(ctx)
}

func (a *Agent) Start(ctx context.Context, initialHubs []HubConfig) error {
	for _, cfg := range initialHubs {
		go a.connectToHub(ctx, cfg, a.statuses)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case stat := <-a.statuses:
			// Wait for one to connect, then return
			if stat.connected {
				a.L.Info("connected to hub", "addr", stat.cfg.Addr)
				return nil
			}
		}
	}
}

func (a *Agent) Wait(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case stat := <-a.statuses:
			if !stat.connected {
				a.L.Warn("connection to hub disconnected", "addr", stat.cfg.Addr)
				time.AfterFunc(10*time.Second, func() {
					go a.connectToHub(ctx, stat.cfg, a.statuses)
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

	id, err := ulid.New(ulid.Now(), mread)
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

	for _, serv := range a.services {
		preamble.Services = append(preamble.Services, &wire.ServiceInfo{
			ServiceId:   serv.Id,
			Type:        serv.Type,
			Description: serv.Description,
			Labels:      serv.Labels,
		})
	}

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

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sessions = append(a.sessions, session)

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

		a.mu.Lock()
		defer a.mu.Unlock()

		for i, sess := range a.sessions {
			if sess == session {
				a.sessions = append(a.sessions[:i], a.sessions[i+1:]...)
				return
			}
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

	var ltrans LogTransmitter
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

func (a *Agent) OpenLogTransmitter(path string) (*LogTransmitter, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var ltrans LogTransmitter
	ltrans.path = path
	ltrans.agent = a

	return &ltrans, nil
}

func (a *Agent) handleStream(ctx context.Context, L hclog.Logger, session *yamux.Session, stream *yamux.Stream, ltrans *LogTransmitter) {
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

	a.mu.RLock()

	serv, ok := a.services[req.TargetService]

	a.mu.RUnlock()

	if !ok {
		L.Error("request received for unknown service", "service", req.TargetService)

		var resp wire.Response
		resp.Error = fmt.Sprintf("unknown service: %s", req.TargetService)

		_, err = fw.WriteMarshal(1, &resp)
		if err != nil {
			L.Error("error marshaling response", "error", err)
			return
		}
	}

	err = serv.Handler.HandleRequest(ctx, L, stream, fr, fw, &req, ltrans)
	if err != nil {
		L.Error("error in service handler", "error", err)
	}
}
