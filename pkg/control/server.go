package control

import (
	context "context"
	"crypto/ed25519"
	"encoding/json"
	fmt "fmt"
	"sync"
	"time"

	"cirello.io/dynamolock"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	"google.golang.org/grpc/metadata"
)

type connectedHub struct {
	xmit chan *pb.CentralActivity
}

type Server struct {
	db       *gorm.DB
	bucket   string
	awsSess  *session.Session
	kmsKeyId string
	privKey  ed25519.PrivateKey
	pubKey   ed25519.PublicKey

	registerToken string

	lockMgr   *dynamolock.Client
	lockTable string

	vaultClient *api.Client
	vaultPath   string
	keyId       string

	hubCert []byte
	hubKey  []byte

	mu            sync.RWMutex
	connectedHubs map[string]*connectedHub
}

func NewServer() (*Server, error) {
	return &Server{
		connectedHubs: make(map[string]*connectedHub),
	}, nil
}

type Account struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string

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

func (s *Server) checkFromHub(ctx context.Context) (*token.ValidToken, error) {
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

	if token.Body.Role != pb.HUB {
		return nil, errors.Wrapf(ErrBadAuthentication, "role was: %s", token.Body.Role)
	}

	return token, nil
}

func (s *Server) AddService(ctx context.Context, service *pb.ServiceRequest) (*pb.ServiceResponse, error) {
	_, err := s.checkFromHub(ctx)
	if err != nil {
		return nil, err
	}

	var so Service
	so.AccountId = service.Account.AccountId.Bytes()
	so.HubId = service.Hub.Bytes()
	so.ServiceId = service.Id.Bytes()
	so.Type = service.Type
	so.Labels = service.Labels.AsStringArray()

	err = dbx.Check(s.db.Create(&so))
	if err != nil {
		return nil, err
	}

	s.broadcastActivity(ctx, &pb.CentralActivity{
		AccountServices: []*pb.AccountServices{
			{
				Account: service.Account,
				Services: []*pb.ServiceRoute{
					{
						Hub:    service.Hub,
						Id:     service.Id,
						Type:   service.Type,
						Labels: service.Labels,
					},
				},
			},
		},
	})
	// return s.s.UserEvent("account-updated", account, false)

	err = s.updateAccountRouting(ctx, service.Account.AccountId.Bytes())
	if err != nil {
		return nil, err
	}

	return &pb.ServiceResponse{}, nil
}

func (s *Server) RemoveService(ctx context.Context, service *pb.ServiceRequest) (*pb.ServiceResponse, error) {
	_, err := s.checkFromHub(ctx)
	if err != nil {
		return nil, err
	}

	err = dbx.Check(s.db.Where("service_id = ?", service.Id.Bytes()).Delete(Service{}))
	if err != nil {
		return nil, err
	}

	err = s.updateAccountRouting(ctx, service.Account.AccountId.Bytes())
	if err != nil {
		return nil, err
	}

	return &pb.ServiceResponse{}, nil
}

func (s *Server) FetchConfig(ctx context.Context, req *pb.ConfigRequest) (*pb.ConfigResponse, error) {
	ctxlog.L(ctx).Info("fetching configuration", "hub", req.Hub.SpecString())

	resp := &pb.ConfigResponse{
		TlsKey:  s.hubKey,
		TlsCert: s.hubCert,
	}

	return resp, nil
}

func (s *Server) StreamActivity(stream pb.ControlServices_StreamActivityServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}

	key := msg.Hub.SpecString()

	ch := &connectedHub{
		xmit: make(chan *pb.CentralActivity),
	}

	s.mu.Lock()
	s.connectedHubs[key] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.connectedHubs, key)
		s.mu.Unlock()

		// drain the xmit channel in the case that the sender saw
		// us around but we're now exiting.
		for {
			select {
			case <-ch.xmit:
				// draining
			default:
				// not blocking
			}
		}
	}()

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case act, ok := <-ch.xmit:
			if !ok {
				return nil
			}

			err = stream.Send(act)
			if err != nil {
				return err
			}
		}
	}
}

func (s *Server) StartActivityReader(ctx context.Context, dbtype, conn string) error {
	ar, err := NewActivityReader(ctx, dbtype, conn)
	if err != nil {
		return err
	}

	go func() {
		L := ctxlog.L(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ar.C:
				if !ok {
					return
				}

				L.Info("detected activity")

				var adds []*pb.AccountServices

				for _, act := range ev {
					var ae pb.ActivityEntry

					err := json.Unmarshal(act.Event, &ae)
					if err != nil {
						L.Error("error unmarshaling activity log entry", "error", err)
						continue
					}

					adds = append(adds, ae.RouteAdded)
				}

				s.broadcastActivity(ctx, &pb.CentralActivity{
					AccountServices: adds,
				})
			}
		}
	}()

	return nil
}

func (s *Server) broadcastActivity(ctx context.Context, act *pb.CentralActivity) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, hub := range s.connectedHubs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case hub.xmit <- act:
			// ok
		}
	}

	return nil
}

type ManagementClient struct {
	ID        []byte `gorm:"primary_key"`
	Namespace string
}

var ErrBadAuthentication = errors.New("bad authentication information presented")

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
	tc.Capabilities = map[string]string{
		token.CapaAccess: rec.Namespace,
	}

	token, err := tc.EncodeED25519WithVault(s.vaultClient, s.vaultPath, s.keyId)
	if err != nil {
		return nil, err
	}

	return &pb.ControlToken{Token: token}, nil
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
	caller, err := s.checkMgmtAllowed(ctx)
	if err != nil {
		return nil, err
	}

	if !caller.AllowAccount(req.Account.Namespace) {
		return nil, errors.Wrapf(ErrInvalidRequest, "invalid namespace requested")
	}

	var ao Account
	ao.ID = req.Account.AccountId.Bytes()
	ao.Namespace = req.Account.Namespace

	de := s.db.Set("gorm:insert_option", "ON CONFLICT DO NOTHING").Create(&ao)

	err = dbx.Check(de)
	if err != nil {
		return nil, err
	}

	var llr LabelLink
	llr.AccountID = req.Account.AccountId.Bytes()
	llr.Labels = FlattenLabels(req.Labels)
	llr.Target = FlattenLabels(req.Target)

	err = dbx.Check(s.db.Create(&llr))
	if err != nil {
		return nil, err
	}

	var out pb.LabelLinks
	out.LabelLinks = []*pb.LabelLink{{
		Account: req.Account,
		Labels:  req.Labels,
		Target:  req.Target,
	}}

	s.broadcastActivity(ctx, &pb.CentralActivity{
		NewLabelLinks: &out,
	})

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
	llr.AccountID = req.Account.AccountId.Bytes()
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
	ao.ID = req.Account.AccountId.Bytes()
	ao.Namespace = req.Account.Namespace

	de := s.db.Set("gorm:insert_option", "ON CONFLICT DO NOTHING").Create(&ao)

	err = dbx.Check(de)
	if err != nil {
		return nil, err
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
