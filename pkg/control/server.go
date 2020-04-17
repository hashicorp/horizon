package control

import (
	context "context"
	"crypto/ed25519"
	fmt "fmt"
	"time"

	"cirello.io/dynamolock"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/serf/serf"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	"google.golang.org/grpc/metadata"
)

type Server struct {
	db       *gorm.DB
	bucket   string
	awsSess  *session.Session
	kmsKeyId string
	privKey  ed25519.PrivateKey
	pubKey   ed25519.PublicKey

	registerToken string

	s *serf.Serf

	lockMgr   *dynamolock.Client
	lockTable string

	vaultClient *api.Client
	vaultPath   string
	keyId       string
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
