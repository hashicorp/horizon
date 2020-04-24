package agent

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/oklog/ulid"
	"github.com/y0ssar1an/q"
)

type ServiceContext interface {
	wire.Context

	ProtocolId() string

	// Returns a reader that reads data on the connection
	BodyReader() io.Reader

	// Returns a writer than writes data on the connection
	BodyWriter() io.Writer

	// Transmit a log message related to this request
	Log(msg *logs.Message) error
}

// The implementation of a service. req is the request sent initially. fr and fw are
// used to read and write data from the client.
type ServiceHandler interface {
	HandleRequest(ctx context.Context, L hclog.Logger, sctx ServiceContext) error
}

type serviceContext struct {
	wire.Context

	protocolId string
	fr         *wire.FramingReader
	stream     *yamux.Stream
	logs       *LogTransmitter

	readHijack  bool
	writeHijack bool
}

func (s *serviceContext) AccountId() *pb.ULID {
	return s.Context.AccountId()
}

var (
	ErrReadHijacked  = errors.New("read hijacked, structured access not available")
	ErrWriteHijacked = errors.New("write hijacked, structured access not available")
)

func (s *serviceContext) ProtocolId() string {
	return s.protocolId
}

func (s *serviceContext) ReadMarshal(v wire.Unmarshaller) (byte, error) {
	if s.readHijack {
		return 0, ErrReadHijacked
	}

	return s.Context.ReadMarshal(v)
}

func (s *serviceContext) WriteMarshal(tag byte, v wire.Marshaller) error {
	if s.writeHijack {
		return ErrReadHijacked
	}

	return s.Context.WriteMarshal(tag, v)
}

// Returns a reader that reads data on the connection
func (s *serviceContext) BodyReader() io.Reader {
	s.readHijack = true
	return s.fr.ReadAdapter()
}

// Returns a writer than writes data on the connection
func (s *serviceContext) BodyWriter() io.Writer {
	s.writeHijack = true
	return s.stream
}

// Transmit a log message related to this request
func (s *serviceContext) Log(msg *logs.Message) error {
	return nil
	// return s.logs.Transmit(msg)
}

// Describes a service that an agent is advertising. When a client connects to the
// service, the Handler will be invoked.
type Service struct {
	// The identifier for the service, populated when the service is added.
	Id *pb.ULID

	// The type identifer for the service. These identifiers are used by other
	// agents and hubs to find services of a particular type.
	Type string

	// The unique identifiers for the service. The labels can be anything and are
	// used by agents and hubs to locate services.
	Labels *pb.LabelSet

	// An additional metadata to attach to the service. Peer agents will be able to
	// see this metadata.
	Metadata map[string]string

	// The handler to invoke when the service is called.
	Handler ServiceHandler
}

type Agent struct {
	L hclog.Logger

	cfg   *yamux.Config
	hosts []string

	LocalAddr string
	Token     string
	Labels    []string

	mu       sync.RWMutex
	services map[string]*Service
	sessions []*yamux.Session

	statuses chan hubStatus
}

func NewAgent(L hclog.Logger) (*Agent, error) {
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
		services: make(map[string]*Service),
		statuses: make(chan hubStatus),
	}

	return agent, nil
}

var mread = ulid.Monotonic(rand.Reader, 1)

func (a *Agent) AddService(serv *Service) (*pb.ULID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	serv.Id = pb.NewULID()

	a.services[serv.Id.SpecString()] = serv
	return serv.Id, nil
}

type HubConfig struct {
	Addr       string
	Insecure   bool
	PinnedCert *x509.Certificate
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

	var clientTlsConfig tls.Config
	clientTlsConfig.NextProtos = []string{"hzn"}
	clientTlsConfig.InsecureSkipVerify = hub.Insecure

	if hub.PinnedCert != nil {
		clientTlsConfig.RootCAs = x509.NewCertPool()
		clientTlsConfig.RootCAs.AddCert(hub.PinnedCert)
	}

	conn, err := tls.Dial("tcp", hub.Addr, &clientTlsConfig)
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
	id := pb.NewULID()

	fw, err := wire.NewFramingWriter(conn)
	if err != nil {
		return err
	}

	defer fw.Recycle()

	fr, err := wire.NewFramingReader(conn)
	if err != nil {
		return err
	}

	var preamble pb.Preamble
	preamble.Token = a.Token
	preamble.SessionId = id.String()
	preamble.Labels = a.Labels

	for _, serv := range a.services {
		var md []*pb.KVPair

		for k, v := range serv.Metadata {
			md = append(md, &pb.KVPair{Key: k, Value: v})
		}

		preamble.Services = append(preamble.Services, &pb.ServiceInfo{
			ServiceId: serv.Id,
			Type:      serv.Type,
			Metadata:  md,
			Labels:    serv.Labels,
		})
	}

	_, err = fw.WriteMarshal(1, &preamble)
	if err != nil {
		return err
	}

	t := time.Now()

	var wc pb.Confirmation
	tag, _, err := fr.ReadMarshal(&wc)
	if err != nil {
		return err
	}

	if tag != 1 {
		return ErrProtocolError
	}

	if wc.Status != "connected" {
		return fmt.Errorf("hub rejected connection: %s", wc.Status)
	}

	latency := time.Since(t)

	L.Debug("connection latency", "latency", latency)

	st := time.Unix(int64(wc.Time.Sec), int64(wc.Time.Nsec))

	skew := time.Since(st)

	bc := &wire.ComposedConn{
		Reader: fr.BufReader(),
		Writer: conn,
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
			if err == yamux.ErrSessionShutdown {
				return
			}

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

	var req pb.SessionIdentification

	tag, _, err := fr.ReadMarshal(&req)
	if err != nil {
		L.Error("error decoding request", "error", err)
		return
	}

	if tag != 1 {
		L.Error("incorrect message tag", "tag", tag)
		return
	}

	targetService := req.ServiceId.SpecString()

	a.mu.RLock()

	q.Q(req.ServiceId, a.services)

	serv, ok := a.services[targetService]

	a.mu.RUnlock()

	if !ok {
		L.Error("request received for unknown service", "service", targetService)

		var resp pb.Response
		resp.Error = fmt.Sprintf("unknown service: %s", targetService)

		_, err = fw.WriteMarshal(255, &resp)
		if err != nil {
			L.Error("error marshaling response", "error", err)
			return
		}

		return
	}

	sctx := &serviceContext{
		Context:    wire.NewContext(nil, fr, fw),
		protocolId: req.ProtocolId,
		fr:         fr,
		stream:     stream,
		logs:       ltrans,
	}

	q.Q(serv)

	err = serv.Handler.HandleRequest(ctx, L, sctx)
	if err != nil {
		L.Error("error in service handler", "error", err)
	}
}
