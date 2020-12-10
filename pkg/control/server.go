package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-metrics"
	"github.com/armon/go-metrics/datadog"
	"github.com/armon/go-metrics/prometheus"
	"github.com/aws/aws-sdk-go/aws/session"
	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-multierror"
	lru "github.com/hashicorp/golang-lru"
	"github.com/hashicorp/horizon/internal/sqljson"
	"github.com/hashicorp/horizon/pkg/dbx"
	_ "github.com/hashicorp/horizon/pkg/grpc/lz4"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/oschwald/geoip2-golang"
	"github.com/pkg/errors"
	"google.golang.org/grpc/metadata"
)

type connectedHub struct {
	mu sync.Mutex

	messages *int64
	bytes    *int64

	activeAgents *int64
	services     *int64
}

// Returns a lock for the given id.
type LockManager interface {
	GetLock(id, val string) (io.Closer, error)
	GetValue(id string) (string, error)
}

type Server struct {
	cfg ServerConfig
	L   hclog.Logger

	bg     context.Context
	cancel func()

	db       *gorm.DB
	bucket   string
	awsSess  *session.Session
	kmsKeyId string
	privKey  ed25519.PrivateKey
	pubKey   ed25519.PublicKey

	registerToken string
	opsToken      string

	lockMgr LockManager

	vaultClient *api.Client
	vaultPath   string
	keyId       string

	hubCert   []byte
	hubKey    []byte
	hubDomain string

	mu            sync.RWMutex
	connectedHubs *lru.ARCCache

	m *metrics.Metrics

	msink metrics.MetricSink

	flowTop *FlowTop

	mux   *http.ServeMux
	asnDB *geoip2.Reader

	hubImageTag string

	hba *HubBroadcastActivity
}

type ServerConfig struct {
	DB *gorm.DB

	Logger hclog.Logger

	RegisterToken string
	OpsToken      string

	VaultClient *api.Client
	VaultPath   string
	KeyId       string

	AwsSession *session.Session
	Bucket     string

	ASNDB string

	HubAccessKey string
	HubSecretKey string

	// The docker image that hubs should be used, this is advertised to the hubs
	// so they can act on it.
	HubImageTag string

	DataDogAddr       string
	DisablePrometheus bool

	LockManager LockManager

	ConsulConfig *consul.Config

	HubCert   []byte
	HubKey    []byte
	HubDomain string
}

func NewServer(cfg ServerConfig) (*Server, error) {
	L := cfg.Logger
	if L == nil {
		L = hclog.L()
	}

	mcfg := metrics.DefaultConfig("control")
	mcfg.EnableHostname = false
	mcfg.EnableRuntimeMetrics = false

	var fanout metrics.FanoutSink

	if !cfg.DisablePrometheus {
		psink, err := prometheus.NewPrometheusSinkFrom(prometheus.PrometheusOpts{
			Expiration: time.Hour,
		})

		if err != nil {
			return nil, err
		}

		fanout = append(fanout, psink)
	}

	msink := metrics.NewInmemSink(time.Minute, time.Hour)
	fanout = append(fanout, msink)

	if cfg.DataDogAddr != "" {
		L.Info("configured to send stats to datadog")
		msink, err := datadog.NewDogStatsdSink(cfg.DataDogAddr, mcfg.HostName)
		if err != nil {
			return nil, err
		}
		fanout = append(fanout, msink)
	}

	me, err := metrics.New(mcfg, fanout)
	if err != nil {
		return nil, err
	}

	flowTop, err := NewFlowTop(DefaultFlowTopSize)
	if err != nil {
		return nil, err
	}

	var (
		hubImageTag  string
		hubImageFile string
	)

	if cfg.HubImageTag != "" && cfg.HubImageTag[0] == '@' {
		L.Info("detected hub image tag is a file, monitoring it for changes")

		hubImageFile = cfg.HubImageTag[1:]

		data, err := ioutil.ReadFile(hubImageFile)
		if err != nil {
			return nil, err
		}

		hubImageTag = string(bytes.TrimSpace(data))
	} else {
		hubImageTag = cfg.HubImageTag
	}

	L.Debug("setting up vault access")
	pub, err := token.SetupVault(cfg.VaultClient, cfg.VaultPath)
	if err != nil {
		return nil, err
	}

	// Setup a token that will be used to connect to hubs
	var tc token.TokenCreator
	tc.Role = pb.CONTROL

	dialToken, err := tc.EncodeED25519WithVault(cfg.VaultClient, cfg.VaultPath, cfg.KeyId)
	if err != nil {
		return nil, err
	}

	ccfg := cfg.ConsulConfig

	if ccfg == nil {
		ccfg = consul.DefaultConfig()
	}

	hba, err := NewHubBroadcastActivity(L, dialToken, ccfg, cfg.HubCert)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	hubsCache, _ := lru.NewARC(100)

	s := &Server{
		bg:            ctx,
		cancel:        cancel,
		cfg:           cfg,
		L:             L,
		db:            cfg.DB,
		vaultClient:   cfg.VaultClient,
		vaultPath:     cfg.VaultPath,
		keyId:         cfg.KeyId,
		registerToken: cfg.RegisterToken,
		opsToken:      cfg.OpsToken,
		awsSess:       cfg.AwsSession,
		bucket:        cfg.Bucket,

		connectedHubs: hubsCache,
		m:             me,
		msink:         msink,
		flowTop:       flowTop,
		mux:           http.NewServeMux(),
		hubImageTag:   hubImageTag,
		hba:           hba,

		hubCert:   cfg.HubCert,
		hubKey:    cfg.HubKey,
		hubDomain: cfg.HubDomain,
	}

	L.Debug("setting up routes")

	s.setupRoutes()

	if cfg.ASNDB != "" {
		L.Debug("loading ASNDB")

		r, err := geoip2.Open(cfg.ASNDB)
		if err == nil {
			s.asnDB = r
		}
	}

	if cfg.LockManager != nil {
		s.lockMgr = cfg.LockManager
	} else {
		s.lockMgr = &inmemLockMgr{}
	}

	s.pubKey = pub

	s.L.Info("vault configured for token signing", "pubkey", hex.EncodeToString(pub))

	if hubImageFile != "" {
		go s.monitorImageFile(hubImageFile)
	}

	go hba.Watch(ctx)

	return s, nil
}

func (s *Server) monitorImageFile(path string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()

	for {
		select {
		case <-s.bg.Done():
			return
		case <-t.C:
			data, err := ioutil.ReadFile(path)
			if err != nil {
				s.L.Error("error reading hub image file: %s", err)
				continue
			}

			tag := string(bytes.TrimSpace(data))

			if s.hubImageTag != tag {
				s.hubImageTag = tag
			}
		}
	}
}

func (s *Server) TokenPub() ed25519.PublicKey {
	return s.pubKey
}

// For management clients to be able valid horizon tokens themselves without having to ask
// the control tier. This allows management clients to piggy back their authentication
// off the horizon tokens as well.
func (s *Server) GetTokenPublicKey(ctx context.Context, _ *pb.Noop) (*pb.TokenInfo, error) {
	return &pb.TokenInfo{PublicKey: s.pubKey}, nil
}

func (s *Server) SetHubTLS(cert, key []byte, domain string) {
	s.hubCert = cert
	s.hubKey = key
	s.hubDomain = domain
}

type Account struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string

	Data sqljson.Data

	CreatedAt time.Time
	UpdatedAt time.Time
}

type Service struct {
	ID int64 `gorm:"primary_key"`

	ServiceId []byte

	HubId []byte

	Account   *Account
	AccountId []byte

	Type        string
	Description string
	Labels      pq.StringArray

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Server) checkFromHub(ctx context.Context, action string) (*token.ValidToken, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, ErrBadAuthentication
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return nil, ErrBadAuthentication
	}

	token, err := token.CheckTokenED25519(auth[0], s.pubKey)
	if err != nil {
		// s.L.Error("error checking token signature", "error", err, "token", auth[0], "pubkey", hex.EncodeToString(s.pubKey))
		return nil, err
	}

	if token.Body.Role != pb.HUB {
		return nil, errors.Wrapf(ErrBadAuthentication, "role was: %s", token.Body.Role)
	}

	s.L.Info("authentication from hub successful", "action", action)

	return token, nil
}

func (s *Server) SyncHub(ctx context.Context, sync *pb.HubSync) (*pb.HubSyncResponse, error) {
	return nil, nil
}

func (s *Server) AddService(ctx context.Context, service *pb.ServiceRequest) (*pb.ServiceResponse, error) {
	_, err := s.checkFromHub(ctx, "add-service")
	if err != nil {
		return nil, err
	}

	s.m.IncrCounter([]string{"service", "add"}, 1)

	var so Service
	so.AccountId = service.Account.Key()
	so.HubId = service.Hub.Bytes()
	so.ServiceId = service.Id.Bytes()
	so.Type = service.Type
	so.Labels = service.Labels.AsStringArray()

	err = dbx.Check(s.db.Create(&so))
	if err != nil {
		return nil, err
	}

	if s.hba != nil {
		s.L.Info("broadcasting service info to hubs", "service", service.Id.SpecString())

		err = s.hba.AdvertiseServices(ctx, &pb.AccountServices{
			Account: service.Account,
			Services: []*pb.ServiceRoute{
				{
					Hub:    service.Hub,
					Id:     service.Id,
					Type:   service.Type,
					Labels: service.Labels,
				},
			},
		})
		if err != nil {
			s.L.Error("error broadcast changes to hubs", "error", err)
		}

	} else {
		s.L.Info("no hub advertising available")
	}

	err = s.updateAccountRouting(ctx, s.db.DB(), service.Account, "add-service")
	if err != nil {
		return nil, err
	}

	return &pb.ServiceResponse{}, nil
}

func (s *Server) RemoveService(ctx context.Context, service *pb.ServiceRequest) (*pb.ServiceResponse, error) {
	_, err := s.checkFromHub(ctx, "remove-service")
	if err != nil {
		return nil, err
	}

	s.m.IncrCounter([]string{"service", "remove"}, 1)

	err = dbx.Check(s.db.Where("service_id = ?", service.Id.Bytes()).Delete(Service{}))
	if err != nil {
		return nil, err
	}

	err = s.updateAccountRouting(ctx, s.db.DB(), service.Account, "remove-service")
	if err != nil {
		return nil, err
	}

	return &pb.ServiceResponse{}, nil
}

func (s *Server) ListServices(ctx context.Context, req *pb.ListServicesRequest) (*pb.ListServicesResponse, error) {
	var services []*Service
	err := dbx.Check(s.db.Where("account_id = ?", req.Account.Key()).Find(&services))
	if err != nil {
		return nil, err
	}

	var resp pb.ListServicesResponse
	for _, svc := range services {
		var labelSet pb.LabelSet
		if err := labelSet.Scan(svc.Labels); err != nil {
			return nil, err
		}

		resp.Services = append(resp.Services, &pb.Service{
			Id:     pb.ULIDFromBytes(svc.ServiceId),
			Hub:    pb.ULIDFromBytes(svc.HubId),
			Type:   svc.Type,
			Labels: &labelSet,
		})
	}

	return &resp, nil
}

func (s *Server) removeHubServices(ctx context.Context, db *gorm.DB, hubId *pb.ULID) error {
	var sos []*Service

	err := dbx.Check(db.Where("hub_id = ?", hubId.Bytes()).Find(&sos))
	if err != nil {
		return err
	}

	err = dbx.Check(db.Where("hub_id = ?", hubId.Bytes()).Delete(Service{}))
	if err != nil {
		return err
	}

	accounts := map[string]struct{}{}

	for _, service := range sos {
		accounts[string(service.AccountId)] = struct{}{}
	}

	s.L.Info("updating account routing", "num-accounts", len(accounts))

	for key := range accounts {
		acc, err := pb.AccountFromKey([]byte(key))
		if err != nil {
			return err
		}

		err = s.updateAccountRouting(ctx, db.DB(), acc, "delete-hub")
		if err != nil {
			return err
		}
	}

	return nil
}

type Hub struct {
	StableID   []byte `gorm:"primary_key"`
	InstanceID []byte

	ConnectionInfo []byte
	LastCheckin    time.Time

	CreatedAt time.Time
}

func (h *Hub) StableIdULID() *pb.ULID {
	return pb.ULIDFromBytes(h.StableID)
}

func (s *Server) FetchConfig(ctx context.Context, req *pb.ConfigRequest) (*pb.ConfigResponse, error) {
	_, err := s.checkFromHub(ctx, "fetch-config")
	if err != nil {
		return nil, err
	}

	L := s.L

	ts := time.Now()

	s.m.IncrCounter([]string{"hub", "connect"}, 1)

	L.Info("fetching configuration", "hub", req.StableId.SpecString())
	defer func() {
		s.m.MeasureSince([]string{"hubs", "fetch_config_time"}, ts)
		L.Info("fetching configuration finished", "hub", req.StableId.SpecString(), "elapse", time.Since(ts))
	}()

	data, err := json.Marshal(req.Locations)
	if err != nil {
		return nil, err
	}

	var hr Hub

	tx := s.db.Begin()

	err = dbx.Check(
		tx.Set("gorm:query_options", "FOR UPDATE").
			Where("stable_id = ?", req.StableId.Bytes()).
			First(&hr),
	)

	if err == gorm.ErrRecordNotFound {
		hr.StableID = req.StableId.Bytes()
		hr.InstanceID = req.InstanceId.Bytes()

		hr.ConnectionInfo = data
		hr.LastCheckin = time.Now()

		err = dbx.Check(tx.Create(&hr))
		if err != nil {
			tx.Rollback()
			return nil, err
		}
	} else {
		prev := pb.ULIDFromBytes(hr.InstanceID)

		if !req.InstanceId.Equal(prev) {
			L.Info("removing previous hub services", "stable", req.StableId, "prev", prev, "new", req.InstanceId, "elapse", time.Since(ts))

			// We nuke the old records from a previous instance_id
			go func() {
				err := s.removeHubServices(s.bg, s.db, prev)
				if err != nil {
					s.L.Error("error removing old hub services", "error", err, "hub", req.StableId)
				}
			}()
		}

		err = dbx.Check(
			tx.Model(&hr).
				Updates(map[string]interface{}{
					"connection_info": data,
					"instance_id":     req.InstanceId.Bytes(),
					"last_checkin":    time.Now(),
				}),
		)

		if err != nil {
			tx.Rollback()
			return nil, err
		}
	}

	err = dbx.Check(tx.Commit())
	if err != nil {
		return nil, err
	}

	resp := &pb.ConfigResponse{
		TlsKey:      s.hubKey,
		TlsCert:     s.hubCert,
		TokenPub:    s.pubKey,
		S3AccessKey: s.cfg.HubAccessKey,
		S3SecretKey: s.cfg.HubSecretKey,
		S3Bucket:    s.cfg.Bucket,
		ImageTag:    s.hubImageTag,
	}

	return resp, nil
}

func (s *Server) HubDisconnect(ctx context.Context, req *pb.HubDisconnectRequest) (*pb.Noop, error) {
	_, err := s.checkFromHub(ctx, "hub-disconnect")
	if err != nil {
		return nil, err
	}

	s.m.IncrCounter([]string{"hub", "disconnect"}, 1)

	s.L.Info("removing hub services", "id", req.StableId)

	serr := s.removeHubServices(ctx, s.db, req.InstanceId)
	if err != nil {
		err = multierror.Append(err, serr)
	}

	s.L.Info("removing hub", "id", req.StableId)

	serr = dbx.Check(s.db.Where("stable_id = ?", req.StableId.Bytes()).Delete(&Hub{}))
	if serr != nil {
		err = multierror.Append(err, serr)
	}

	s.L.Info("hub cleaned up", "possible-error", err)

	return &pb.Noop{}, err
}

func (s *Server) ProcessHubStats(ctx context.Context, hs *pb.HubStatsRequest) (*pb.Noop, error) {
	ch, err := s.trackHub(hs.Hub)
	if err != nil {
		return nil, err
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	s.processFlows(ch, hs.Flows)

	return &pb.Noop{}, nil
}

func (s *Server) processFlows(ch *connectedHub, flows []*pb.FlowRecord) {
	var mdiff, bdiff int64

	for _, rec := range flows {
		if rec.Stream != nil {
			mdiff += rec.Stream.NumMessages
			bdiff += rec.Stream.NumBytes

			labels := []metrics.Label{
				{
					Name:  "flow",
					Value: rec.Stream.FlowId.SpecString(),
				},
				{
					Name:  "hub",
					Value: rec.Stream.HubId.SpecString(),
				},
				{
					Name:  "agent",
					Value: rec.Stream.AgentId.SpecString(),
				},
				{
					Name:  "service",
					Value: rec.Stream.ServiceId.SpecString(),
				},
				{
					Name:  "account",
					Value: rec.Stream.Account.SpecString(),
				},
			}

			s.m.IncrCounterWithLabels([]string{"stream", "messages"}, float32(rec.Stream.NumMessages), labels)
			s.m.IncrCounterWithLabels([]string{"stream", "bytes"}, float32(rec.Stream.NumBytes), labels)

			s.flowTop.Add(rec.Stream)
		}

		if rec.Agent != nil {
			labels := []metrics.Label{
				{
					Name:  "hub",
					Value: rec.Agent.HubId.SpecString(),
				},
				{
					Name:  "agent",
					Value: rec.Agent.AgentId.SpecString(),
				},
				{
					Name:  "account",
					Value: rec.Agent.Account.SpecString(),
				},
			}

			s.m.SetGaugeWithLabels([]string{"hub", "streams"}, float32(rec.Agent.ActiveStreams), labels)
		}

		if rec.HubStats != nil {
			atomic.StoreInt64(ch.activeAgents, rec.HubStats.ActiveAgents)
			atomic.StoreInt64(ch.services, rec.HubStats.Services)

			labels := []metrics.Label{
				{
					Name:  "hub",
					Value: rec.HubStats.HubId.SpecString(),
				},
			}

			s.m.SetGaugeWithLabels([]string{"agents", "active"}, float32(rec.HubStats.ActiveAgents), labels)
		}
	}

	atomic.AddInt64(ch.messages, mdiff)
	atomic.AddInt64(ch.bytes, bdiff)

	s.m.IncrCounter([]string{"total", "messages"}, float32(mdiff))
	s.m.IncrCounter([]string{"total", "bytes"}, float32(bdiff))

	// Reread calculate the total active agents and services
	s.mu.Lock()
	defer s.mu.Unlock()

	/*

		TODO(evanphx) restore these metrics as ones derived from talking to live hubs

		var agents, services float32

		for _, h := range s.connectedHubs {
			agents += float32(atomic.LoadInt64(h.activeAgents))
			services += float32(atomic.LoadInt64(h.services))
		}

		s.m.SetGauge([]string{"hubs", "agents", "active"}, agents)
		s.m.SetGauge([]string{"hubs", "services"}, services)

	*/
}

func (s *Server) trackHub(id *pb.ULID) (*connectedHub, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := id.SpecString()

	ch, ok := s.connectedHubs.Get(key)
	if ok {
		return ch.(*connectedHub), nil
	}

	rec := &connectedHub{
		messages: new(int64),
		bytes:    new(int64),

		activeAgents: new(int64),
		services:     new(int64),
	}

	s.connectedHubs.Add(key, rec)

	return rec, nil
}

type ManagementClient struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string
}

var ErrBadAuthentication = errors.New("bad authentication information presented")

func (s *Server) GetManagementToken(ctx context.Context, namespace string) (string, error) {
	var rec ManagementClient

	err := dbx.Check(s.db.Where("namespace LIKE ?", namespace+"%").First(&rec))
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return "", err
		}
		rec.ID = pb.NewULID().Bytes()
		rec.Namespace = namespace

		err = dbx.Check(s.db.Create(&rec))
		if err != nil {
			return "", err
		}
	}

	var tc token.TokenCreator
	tc.Role = pb.MANAGE
	tc.Capabilities = map[pb.Capability]string{
		pb.ACCESS: namespace,
	}

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return "", err
	}

	return token, nil
}

func (s *Server) Register(ctx context.Context, reg *pb.ControlRegister) (*pb.ControlToken, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, ErrBadAuthentication
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return nil, ErrBadAuthentication
	}

	if auth[0] != s.registerToken {
		return nil, ErrBadAuthentication
	}

	var rec ManagementClient

	err := dbx.Check(s.db.Where("namespace LIKE ?", reg.Namespace+"%").First(&rec))
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("namespace already in use")
	}

	rec.ID = pb.NewULID().Bytes()
	rec.Namespace = reg.Namespace

	err = dbx.Check(s.db.Create(&rec))
	if err != nil {
		return nil, err
	}

	var tc token.TokenCreator
	tc.Role = pb.MANAGE
	tc.Capabilities = map[pb.Capability]string{
		pb.ACCESS: rec.Namespace,
	}

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return nil, err
	}

	return &pb.ControlToken{Token: token}, nil
}

func (s *Server) LocalIssueHubToken() (string, error) {
	var tc token.TokenCreator
	tc.Role = pb.HUB

	return tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
}

func (s *Server) IssueHubToken(ctx context.Context, _ *pb.Noop) (*pb.CreateTokenResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, ErrBadAuthentication
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return nil, ErrBadAuthentication
	}

	if auth[0] != s.registerToken {
		return nil, ErrBadAuthentication
	}

	var tc token.TokenCreator
	tc.Role = pb.HUB

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return nil, err
	}

	return &pb.CreateTokenResponse{Token: token}, nil
}

func (s *Server) checkMgmtAllowed(ctx context.Context) (*token.ValidToken, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, ErrBadAuthentication
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return nil, ErrBadAuthentication
	}

	token, err := token.CheckTokenED25519(auth[0], s.pubKey)
	if err != nil {
		return nil, err
	}

	if token.Body.Role != pb.MANAGE {
		return nil, ErrBadAuthentication
	}

	return token, nil
}

func (s *Server) AddAccount(ctx context.Context, req *pb.AddAccountRequest) (*pb.Noop, error) {
	L := s.L.Named("add-account")

	L.Info("adding new account",
		"account", req.Account.SpecString(),
		"limits", req.Limits.String(),
	)

	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		L.Error("error checking mgmt token", "err", err)
		return nil, err
	}

	s.m.IncrCounter([]string{"account", "create"}, 1)

	if req.Account.Namespace == "" {
		req.Account.Namespace = caller.Account().Namespace
	}

	if !caller.AllowAccount(req.Account.Namespace) {
		L.Error(
			"rejected access to account based on caller namespace",
			"caller-namespace", caller.Account().Namespace,
			"requested-namespace", req.Account.Namespace,
		)

		return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested")
	}

	var ao Account
	ao.ID = req.Account.Key()
	ao.Namespace = req.Account.Namespace
	err = ao.Data.Set("limits", req.Limits)
	if err != nil {
		return nil, errors.Wrapf(ErrInvalidRequest, "error parsing limits: %s", err)
	}

	de := s.db.Create(&ao)

	err = dbx.Check(de)
	if err != nil {
		L.Error("error reading account information for labellink", "error", err)
		return nil, err
	}

	return &pb.Noop{}, nil
}

type LabelLink struct {
	ID int `gorm:"primary_key"`

	Account   *Account
	AccountID []byte

	Labels string
	Target string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Server) AddLabelLink(ctx context.Context, req *pb.AddLabelLinkRequest) (*pb.Noop, error) {
	L := s.L.Named("add-label-link")

	L.Info("adding new label-link",
		"account", req.Account.SpecString(),
		"labels", req.Labels.SpecString(),
		"target", req.Target.SpecString(),
	)

	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		L.Error("error checking mgmt token", "err", err)
		return nil, err
	}

	if req.Account.Namespace == "" {
		req.Account.Namespace = caller.Account().Namespace
	}

	if !caller.AllowAccount(req.Account.Namespace) {
		L.Error(
			"rejected access to account based on caller namespace",
			"caller-namespace", caller.Account().Namespace,
			"requested-namespace", req.Account.Namespace,
		)

		return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested")
	}

	var ao Account

	de := s.db.First(&ao, req.Account.Key())

	err = dbx.Check(de)
	if err != nil {
		L.Error("error reading account information for label-link", "error", err)
		return nil, errors.Wrapf(err, "account for label-link not found")
	}

	L.Trace("account for label-link initialized correctly")

	var llr LabelLink
	llr.AccountID = req.Account.Key()
	llr.Labels = FlattenLabels(req.Labels)
	llr.Target = FlattenLabels(req.Target)

	err = dbx.Check(s.db.Create(&llr))
	if err != nil {
		L.Error("error creating label-link record", "error", err)
		return nil, err
	}

	L.Trace("label-link saved to database")

	var pblimit pb.Account_Limits
	ao.Data.Get("limits", &pblimit)

	var out pb.LabelLinks
	out.LabelLinks = []*pb.LabelLink{{
		Account: req.Account,
		Labels:  req.Labels,
		Target:  req.Target,
		Limits:  &pblimit,
	}}

	if s.hba != nil {
		L.Trace("broadcasting new label-link activity")
		err = s.hba.AdvertiseLabelLinks(ctx, &out)
		if err != nil {
			L.Error("error broadcasting new label links", "error", err)
		}
	}

	err = s.updateLabelLinks(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.Noop{}, nil
}

func (s *Server) RemoveLabelLink(ctx context.Context, req *pb.RemoveLabelLinkRequest) (*pb.Noop, error) {
	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		return nil, err
	}

	if !caller.AllowAccount(req.Account.Namespace) {
		return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested")
	}

	var llr LabelLink
	llr.AccountID = req.Account.Key()
	llr.Labels = FlattenLabels(req.Labels)

	err = dbx.Check(s.db.
		Where("account_id = ?", llr.AccountID).
		Where("labels = ?", FlattenLabels(req.Labels)).
		Delete(&LabelLink{}),
	)

	if err != nil {
		return nil, err
	}

	err = s.updateLabelLinks(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.Noop{}, nil
}

var ErrInvalidRequest = errors.New("invalid request")

func (s *Server) CreateToken(ctx context.Context, req *pb.CreateTokenRequest) (*pb.CreateTokenResponse, error) {
	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		return nil, err
	}

	if !caller.AllowAccount(req.Account.Namespace) {
		return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested")
	}

	// If the caller is requesting access capability, make sure it's under the callers namespace
	for _, cb := range req.Capabilities {
		if cb.Capability == pb.ACCESS {
			if !caller.AllowAccount(cb.Value) {
				return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested in access capability")
			}
		}
	}

	var dur time.Duration

	if req.ValidDuration != nil {
		dur = req.ValidDuration.ToDuration()
	}

	var ao Account
	ao.ID = req.Account.Key()
	ao.Namespace = req.Account.Namespace

	de := s.db.Set("gorm:insert_option", "ON CONFLICT (id) DO UPDATE SET namespace = EXCLUDED.namespace").Create(&ao)

	err = dbx.Check(de)
	if err != nil {
		if err != sql.ErrNoRows {
			return nil, errors.Wrapf(err, "creating account record")
		}
	}

	var tc token.TokenCreator
	tc.AccountId = req.Account.AccountId
	tc.AccuntNamespace = req.Account.Namespace
	tc.RawCapabilities = req.Capabilities
	tc.ValidDuration = dur

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return nil, err
	}

	return &pb.CreateTokenResponse{Token: token}, nil
}

const DefaultListAccountsLimit = 100

func (s *Server) ListAccounts(ctx context.Context, req *pb.ListAccountsRequest) (*pb.ListAccountsResponse, error) {
	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		return nil, err
	}

	ok, ns := caller.HasCapability(pb.ACCESS)
	if !ok {
		return nil, ErrInvalidRequest
	}

	s.L.Info("list accounts request", "namespace", ns)

	var accounts []*Account

	limit := req.Limit
	if limit == 0 {
		limit = DefaultListAccountsLimit
	}

	if len(req.Marker) > 0 {
		err = dbx.Check(
			s.db.Where("id > ?", req.Marker).
				Where("namespace = ? OR starts_with(namespace, ?)", ns, ns+"/").
				Limit(limit).Order("id ASC").
				Find(&accounts),
		)
	} else {
		err = dbx.Check(
			s.db.
				Where("namespace = ? OR starts_with(namespace, ?)", ns, ns+"/").
				Limit(limit).Order("id ASC").
				Find(&accounts),
		)
	}

	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
	}

	var resp pb.ListAccountsResponse
	if len(accounts) == 0 {
		return &resp, nil
	}

	resp.NextMarker = accounts[len(accounts)-1].ID

	for _, acc := range accounts {
		acc, err := pb.AccountFromKey(acc.ID)
		if err != nil {
			return nil, err
		}

		resp.Accounts = append(resp.Accounts, acc)
	}

	return &resp, nil
}

func (s *Server) AllHubs(ctx context.Context, _ *pb.Noop) (*pb.ListOfHubs, error) {
	var hubs []*Hub

	err := dbx.Check(s.db.Find(&hubs))
	if err != nil {
		return nil, err
	}

	var out pb.ListOfHubs

	for _, h := range hubs {
		var locs []*pb.NetworkLocation

		err = json.Unmarshal(h.ConnectionInfo, &locs)
		if err != nil {
			return nil, err
		}

		out.Hubs = append(out.Hubs, &pb.HubInfo{
			Id:        pb.ULIDFromBytes(h.InstanceID),
			Locations: locs,
		})
	}

	return &out, nil
}

func (s *Server) RequestServiceToken(ctx context.Context, req *pb.ServiceTokenRequest) (*pb.ServiceTokenResponse, error) {
	_, err := s.checkFromHub(ctx, "request-service-token")
	if err != nil {
		return nil, err
	}

	var tc token.TokenCreator
	tc.AccountId = pb.InternalAccount
	tc.AccuntNamespace = req.Namespace
	tc.RawCapabilities = []pb.TokenCapability{
		{
			Capability: pb.ACCESS,
			Value:      req.Namespace,
		},
		{
			Capability: pb.CONNECT,
		},
	}

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return nil, err
	}

	return &pb.ServiceTokenResponse{Token: token}, nil
}
