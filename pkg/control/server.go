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

// Called when a client doesn't present a valid token
var ErrBadAuthentication = errors.New("bad authentication information presented")

// Small bit of state used by Server to track information about hubs it
// knows about. In the past, this was managed by the ActivityStream but now
// it's updated when new FlowRecords are sent by a hub. The information is just
// for statistical tracking, it doesn't influence functionality.
type connectedHub struct {
	mu sync.Mutex

	messages *int64
	bytes    *int64

	activeAgents *int64
	services     *int64
}

// LockManager is used by Server to lock access to an S3 routing blob.
// This locking prevents 2 instances of Server from accidentally clobbering changes.
type LockManager interface {
	// Returns a lock for the given id, setting the lock's value to val.
	GetLock(id, val string) (io.Closer, error)

	// Returns the previously set value for the lock.
	GetValue(id string) (string, error)
}

// Server represents the top of the control Server stack. It is mostly a gRPC
// server implementation that responds to calls made by hubs. In the past it did
// some coordinating with peer Servers (running in the same cluster as other instances
// of the same code) but since move to having control call back to hubs via gRPC, Server
// has gotten simpler, mostly stateless reacting to gRPC commands, checking the database,
// updating routing blobs in S3, and calling back to hubs if need be.
type Server struct {
	cfg ServerConfig
	L   hclog.Logger

	// This context is used by operations that outlive a request, but we still want to be able
	// to cancel on a Server shutdown.
	bg     context.Context
	cancel func()

	db       *gorm.DB
	bucket   string
	awsSess  *session.Session
	kmsKeyId string

	// These are keys used to sign and validate tokens. When Vault is used (the production
	// default) only pubKey will be populated and used though.
	privKey ed25519.PrivateKey
	pubKey  ed25519.PublicKey

	// These are the static, opaque tokens used to access functionality within Server itself,
	// such as the ability to create a new management client. These tokens are internal only.
	registerToken string
	opsToken      string

	lockMgr LockManager

	// How to talk to vault. We use vault to store the token signing key (as a transit value)
	// and vault KV to store the TLS data used by the hubs.
	vaultClient *api.Client
	vaultPath   string
	keyId       string

	// These are the settings that are sent down to the hubs when they bootstrap their
	// config. Cert and Key are what they use for their own TLS server.
	hubCert   []byte
	hubKey    []byte
	hubDomain string

	// mu protects access to connectedHubs
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
	// The connection to the database to use. Currently only supports postgresql.
	DB *gorm.DB

	// Logger to send logging information to
	Logger hclog.Logger

	// The internal token used to protect the endpoints to create management clients
	RegisterToken string

	// The internal token used to protect access to stats and health endpoints
	OpsToken string

	// A connection to the Vaulrt cluster to use
	VaultClient *api.Client

	// The path to the transit key used to sign and validate tokens
	VaultPath string

	// The identifier for the key in use. This is just stamped into the tokens so we know
	// what key was used to sign them. This is unrelated to any vault values, it's just for
	// horizon devs to know.
	KeyId string

	// Session to connect to S3 with for accessing the routing blobs
	AwsSession *session.Session

	// The S3 bucket to store and retrieve the routing blobs from
	Bucket string

	// A filepath on local disk to read the ASN database. This used by control to
	// figure out the network of hubs for help optimizing their connection patterns.
	// If not set, that functionality is disabled.
	ASNDB string

	// The AWS IAM Access key to give to the hubs to use to access the S3 routing blobs
	HubAccessKey string

	// The matching secret key to HubAccessKey
	HubSecretKey string

	// The docker image that hubs should be used, this is advertised to the hubs
	// so they can act on it.
	HubImageTag string

	// The address of the datadog statsd sink to send metrics to. If not set, data
	// will not be sent to DataDog.
	DataDogAddr string

	// Controls if gather and advertising stats via prometheus is available. This
	// is configurable to allow tests to disable this functionality as it sets up
	// some global registry stuff.
	DisablePrometheus bool

	// The LockManager to use for protecting access to the S3 routing blobs
	LockManager LockManager

	// The configuration to connect to Consul with. Normally this is just consul.DefaultConfig()
	ConsulConfig *consul.Config

	// The TLS Certificate sent to the hubs for them to use on port 443
	HubCert []byte

	// The private key matching the cert in HubCert
	HubKey []byte

	// A domain name the hubs are under. This doesn't have to be a true DNS setup domain,
	// just one that uniquely identifies the hubs using this control system.
	HubDomain string
}

// Setup a new Server given a ServerConfig.
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

	// We always have the metrics internally, even if we aren't going to send them anywhere.
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

	// Setup a token that will be used to connect to hubs via gRPC
	var tc token.TokenCreator
	tc.Role = pb.CONTROL

	dialToken, err := tc.EncodeED25519WithVault(cfg.VaultClient, cfg.VaultPath, cfg.KeyId)
	if err != nil {
		return nil, err
	}

	// Configure our ability to broadcast to the hubs with knowledge from consul
	ccfg := cfg.ConsulConfig

	if ccfg == nil {
		ccfg = consul.DefaultConfig()
	}

	hba, err := NewHubBroadcastActivity(L, dialToken, ccfg, cfg.HubCert)
	if err != nil {
		return nil, err
	}

	// Our own background context that we can cancel when we shutdown
	bg, cancel := context.WithCancel(context.Background())

	// a cache of connectedHub values. We don't delete these yet, that's why we store
	// them in a cache, to prevent a memory leak.
	hubsCache, _ := lru.NewARC(100)

	s := &Server{
		bg:            bg,
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

		pubKey: pub,
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

	s.L.Info("vault configured for token signing", "pubkey", hex.EncodeToString(pub))

	// If the hub image is set, we're going to monitor that file in the background.
	if hubImageFile != "" {
		go s.monitorImageFile(hubImageFile)
	}

	// Start monitoring consul and keeping our cache up to date.
	go hba.Watch(bg)

	return s, nil
}

func (s *Server) monitorImageFile(path string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()

	for {
		select {

		// The server is shutting down, cleanup.
		case <-s.bg.Done():
			return

		// A minute has passed.
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

// TokenPub returns the public key used to validate the authentication toknes
func (s *Server) TokenPub() ed25519.PublicKey {
	return s.pubKey
}

// GetTokenPublicKey is called by management clients to get the key used to validate tokens.
// For management clients to be able valid horizon tokens themselves without having to ask
// the control tier. This allows management clients to piggy back their authentication
// off the horizon tokens as well.
func (s *Server) GetTokenPublicKey(ctx context.Context, _ *pb.Noop) (*pb.TokenInfo, error) {
	return &pb.TokenInfo{PublicKey: s.pubKey}, nil
}

// SetHubTLS updates the cert, key, and domain that hubs should use. The next time a hub
// connects and fetches it's configuration, these values will be returned to it.
func (s *Server) SetHubTLS(cert, key []byte, domain string) {
	s.hubCert = cert
	s.hubKey = key
	s.hubDomain = domain
}

// Account is used as a gorm model to manage account information in the database.
type Account struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string

	Data sqljson.Data

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service is a gorm model. Each entry represents a live service currently available
// a hub.
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

// checkFromHub extracts gRPC authorization from ctx and makes sure there is a token attached.
// The token must have a HUB role, indicating it's a call from a hub.
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

// AddService is called a hub to register a service an agent connected to that hub is advertising.
// The new service is restored in the database and S3 routing database. Additionally, the information
// about these routes is broadcast to all the hubs to allow them to use the new information immediately.
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

// RemoveService deletes a given service from the database and the S3 routing information.
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

// ListServices returns all services currently present in the database.
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

// removeHubServices deletes all the services that were registered to the given hub, specified
// by the hub's instance id. This also updates the S3 routing information to remove the services.
// Because the number of services could be quite high, this function can be slow as it updates
// all the S3 routing blobs, one per account (the services will be spread across many accounts).
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

	// To avoid updating the S3 routing for each service, we instead track all the unique
	// accounts for all the services we see, and just update the routing for those specific
	// accounts. For accounts with a high number of services, this is a huge win in terms of
	// efficiency.
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

// Hub is used as a gorm model, providing information about known hubs.
type Hub struct {
	StableID   []byte `gorm:"primary_key"`
	InstanceID []byte

	ConnectionInfo []byte
	LastCheckin    time.Time

	CreatedAt time.Time
}

// StableIdULID parses the StableID field into a ULID, which is the stable identifier ULID
// the hub advertised itself as. Stable IDs are constant across hub restarts.
func (h *Hub) StableIdULID() *pb.ULID {
	return pb.ULIDFromBytes(h.StableID)
}

// InstanceULID parses the InstanceID field into a ULID, which is the instance identifier ULID
// the hub advertised itself as. Instance IDs change on every hub start, even if the compute node
// the hub is on is the same as last time (ie a hub restart).
func (h *Hub) InstanceULID() *pb.ULID {
	return pb.ULIDFromBytes(h.InstanceID)
}

// FetchConfig validates the request is coming from a hub to get the hub's configuration. This function
// creates a Hub database record with the information in the ConfigRequest. It also detects if
// the hub is already known but restarted (different instance ids) and removes the hubs old service
// records to keep the routing information correct.
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

	// If we don't find a record with the given stable id, create a new one.
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

		// If there is already a hub record, then see if it has this same instance id value too. If not, then
		// the hub has restarted and we need to cleanup it's old records.
		if !req.InstanceId.Equal(prev) {
			L.Info("removing previous hub services", "stable", req.StableId, "prev", prev, "new", req.InstanceId, "elapse", time.Since(ts))

			// We nuke the old records from a previous instance_id. We do this in the background because
			// it can be a fairly slow process if there are a large number of accounts represented (due to having
			// to update the S3 routing for each of them)
			go func() {
				err := s.removeHubServices(s.bg, s.db, prev)
				if err != nil {
					s.L.Error("error removing old hub services", "error", err, "hub", req.StableId)
				}
			}()
		}

		// This is gorm partly update. It's weird and I wonder if we should just be using raw SQL.
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

	// Return the configuration information the hub should use to access S3 and TLS configuration
	// params.
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

// HubDisconnect is called by a hub when it has shutdown cleanly (and thus is rarely called in production).
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

// ProcessHubStats is called by a hub when it has some stats it thinks the control should track.
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

// ManagementClient is used as a gorm model, representing a management client that
type ManagementClient struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string
}

// GetManagementToken is test helper method to create a new management client and return a token
// for it to use.
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

// Register is called by an internal tool with the register token to create a new ManagementClient
// record. It returns the token that the management client should use with further operations.
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

// LocalIssueHubToken is a test helper function to generate a token that can be used
// by a hub to talk with control.
func (s *Server) LocalIssueHubToken() (string, error) {
	var tc token.TokenCreator
	tc.Role = pb.HUB

	return tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
}

// IssueHubToken is called with the register token for authentication. It generates a new
// token with the HUB role and returns it.
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

// checkMgmtAllowed inspects the ctx and extracts the gRPC authentication information.
// It then validates that the token is valid and has the MANAGE role, indicating it's
// being used by a management client.
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

// AddAccount is called by manegement clients to create a new account. The request includes
// some information about the capabilities and limits the new account should have.
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

// LabelLink is used as a gorm model to store information about a label link.
type LabelLink struct {
	ID int `gorm:"primary_key"`

	Account   *Account
	AccountID []byte

	Labels string
	Target string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddLabelLink is called by a management client to register a new LabelLink. LabelLinks are used
// by services such as the hub http forwarder to map hostnames to accounts and sets of services.
// It also updates the S3 LabelLink object.
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

	// If we can, tell all the hubs about this new labellink. In the case of the hub web forwarder, this
	// allows them to quickly pickup new hostname mappings.
	if s.hba != nil {
		L.Trace("broadcasting new label-link activity")
		err = s.hba.AdvertiseLabelLinks(ctx, &out)
		if err != nil {
			L.Error("error broadcasting new label links", "error", err)
		}
	}

	L.Trace("running s3 update of label links in background")
	go func() {
		err := s.updateLabelLinks(s.bg)
		if err != nil {
			L.Error("error updating label links in S3", "error", err)
		}
	}()

	return &pb.Noop{}, nil
}

// RemoveLabelLink is called by a management client. It deletes any label links that match the request.
// It also updates the S3 LabelLink object.
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

	s.L.Trace("running s3 update of label links in background")
	go func() {
		err := s.updateLabelLinks(s.bg)
		if err != nil {
			s.L.Error("error updating label links in S3", "error", err)
		}
	}()

	return &pb.Noop{}, nil
}

var ErrInvalidRequest = errors.New("invalid request")

// CreateToken is called by a management client. It generates a new token for an account (validated as
// an account the management client is authorized to). The request includes capabilities for the token as well.
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

// ListAccounts is called by a management client. It returns all the accounts that the management client
// has previously registered.
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

// AllHubs is an authentication-less API that returns all available hubs from the database.
func (s *Server) AllHubs(ctx context.Context, _ *pb.Noop) (*pb.ListOfHubs, error) {
	var hubs []*Hub

	err := dbx.Check(s.db.Find(&hubs))
	if err != nil {
		return nil, err
	}

	var out pb.ListOfHubs

	for _, h := range hubs {
		if s.hba != nil && !s.hba.HubAvailable(h.InstanceULID()) {
			continue
		}
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

// RequestServiceToken is called by a hub to generate an internal token that used by internal services
// to access accounts in a namespace. This is used by the hub's web forwarder to lookup services matching
// a labellink.
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
