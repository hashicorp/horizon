package registry

import (
	"crypto/rand"
	"errors"
	"io"
	mathrand "math/rand"
	"sort"
	"strings"
	"sync"

	petname "github.com/dustinkirkland/golang-petname"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/data"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/oklog/ulid"
)

type Registry struct {
	key    []byte
	suffix string
	mu     sync.RWMutex

	sessions  map[string]map[string]map[string][]*wire.ServiceInfo
	agentKeys map[string]string

	storage *data.Bolt
}

func RandomKey() []byte {
	key := make([]byte, 64)

	_, err := io.ReadFull(rand.Reader, key)
	if err != nil {
		panic(err)
	}

	return key
}

func NewRegistry(key []byte, suffix string, storage *data.Bolt) (*Registry, error) {
	reg := &Registry{
		suffix:    suffix,
		storage:   storage,
		sessions:  make(map[string]map[string]map[string][]*wire.ServiceInfo),
		agentKeys: make(map[string]string),
	}

	petname.NonDeterministicMode()

	return reg, nil
}

var ErrNoTarget = errors.New("unknown target")

func (r *Registry) CertDecision(L hclog.Logger, name string) error {
	if !r.storage.KnownTarget(name) {
		L.Info("unknown target requested", "name", name)
		return ErrNoTarget
	}

	L.Info("cert needed for target", "name", name)

	return nil
}

const ulidSize = 16

var ErrBadToken = errors.New("bad token detected")

func (r *Registry) verifyToken(L hclog.Logger, stoken string) (ulid.ULID, *token.Headers, error) {
	L.Debug("verifying token", "token", stoken)

	headers, err := token.CheckTokenHMAC(stoken, r.key)
	if err != nil {
		return ulid.ULID{}, nil, err
	}

	id := headers.AccountId()

	L.Debug("token account", "id", id.String())

	return id, headers, nil
}

var mrand = ulid.Monotonic(rand.Reader, 1)

func (r *Registry) AddAccount(L hclog.Logger) (ulid.ULID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	defTarget := petname.Generate(3, "-") + r.suffix

	id, err := ulid.New(ulid.Now(), mrand)
	if err != nil {
		return ulid.ULID{}, err
	}

	r.storage.AddAccount(id.String(), defTarget)

	L.Info("created account", "id", id.String(), "def-target", defTarget)

	return id, nil
}

func compressLabels(v []string) string {
	sort.Strings(v)

	return strings.ToLower(strings.Join(v, ", "))
}

const HTTPType = "http"

func (r *Registry) AuthAgent(L hclog.Logger, stoken, pubKey string, labels []string, services []*wire.ServiceInfo) (string, *token.Headers, func(), error) {
	accId, headers, err := r.FindAccount(L, stoken)
	if err != nil {
		return "", nil, nil, err
	}

	id, err := ulid.New(ulid.Now(), mrand)
	if err != nil {
		return "", nil, nil, err
	}

	agentKey := id.String()

	labelKey := compressLabels(labels)

	L.Info("agent authenticated", "id", id.String(), "label-key", labelKey, "account-id", accId.String())

	r.mu.Lock()
	defer r.mu.Unlock()

	r.agentKeys[agentKey] = pubKey

	account := r.sessions[accId.String()]
	if account == nil {
		account = make(map[string]map[string][]*wire.ServiceInfo)
		r.sessions[accId.String()] = account
	}

	for _, serv := range services {
		servLabels := compressLabels(serv.Labels)

		sessions := account[servLabels]
		if sessions == nil {
			sessions = make(map[string][]*wire.ServiceInfo)
			account[servLabels] = sessions
		}

		sessions[agentKey] = append(sessions[agentKey], serv)
	}

	remove := func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		for _, serv := range services {
			servLabels := compressLabels(serv.Labels)

			sessions := account[servLabels]
			delete(sessions, agentKey)
		}
	}

	// Create a default route and tag it with the given labels

	var created bool

	for _, serv := range services {
		if serv.Type == HTTPType {
			created, err = r.storage.CreateDefaultRoute(accId.String(), compressLabels(serv.Labels))
			if err != nil {
				return "", nil, nil, err
			}

			if created {
				break
			}
		}
	}

	if created {
		L.Info("created default route", "account", accId.String(), "label-key", labelKey)
	}

	return agentKey, headers, remove, nil
}

var (
	ErrNoAccount = errors.New("no account found")
	ErrNoRoute   = errors.New("no route found")
	ErrNoService = errors.New("no service found")
)

func (r *Registry) Token(L hclog.Logger, accId ulid.ULID) (string, error) {
	var tc token.TokenCreator
	tc.AccountId = accId[:]

	token, err := tc.EncodeHMAC(r.key)
	if err != nil {
		return "", err
	}

	L.Debug("created token", "account-id", accId.String())

	return token, nil
}

func (r *Registry) TokenWithMetadata(L hclog.Logger, accId ulid.ULID, hdrs map[string]string) (string, error) {
	var tc token.TokenCreator
	tc.AccountId = accId[:]
	tc.Metadata = hdrs

	token, err := tc.EncodeHMAC(r.key)
	if err != nil {
		return "", err
	}

	L.Debug("created token", "account-id", accId.String())

	return token, nil
}

func (r *Registry) FindAccount(L hclog.Logger, token string) (ulid.ULID, *token.Headers, error) {
	accId, headers, err := r.verifyToken(L, token)
	if err != nil {
		return ulid.ULID{}, nil, err
	}

	if !r.storage.CheckAccount(accId.String()) {
		return ulid.ULID{}, nil, ErrNoAccount
	}

	return accId, headers, nil
}

type ResolvedService struct {
	Agent       string
	AgentKey    string
	ServiceId   string
	ServiceType string
}

func (r *Registry) ResolveAgent(L hclog.Logger, target string) (ResolvedService, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var rs ResolvedService

	accId, labelKey, err := r.storage.LabelsForTarget(target)
	if err != nil {
		return rs, err
	}

	if accId == "" || labelKey == "" {
		return rs, ErrNoAccount
	}

	account, ok := r.sessions[accId]
	if !ok {
		L.Error("account missing", "id", accId)
		return rs, ErrNoAccount
	}

	sessions, ok := account[labelKey]
	if !ok {
		return rs, ErrNoRoute
	}

	pick := mathrand.Intn(len(sessions))

	i := 0
	for k, services := range sessions {
		if i != pick {
			i++
			continue
		}

		rs.Agent = k
		rs.AgentKey = r.agentKeys[k]

		service := services[mathrand.Intn(len(services))]

		rs.ServiceId = service.ServiceId.String()
		rs.ServiceType = service.Type
		break
	}

	if rs.Agent == "" {
		return rs, ErrNoService
	}

	return rs, nil
}

func (r *Registry) MatchServices(accId string, labels []string) ([]ResolvedService, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	account, ok := r.sessions[accId]
	if !ok {
		return nil, ErrNoAccount
	}

	labelKey := compressLabels(labels)

	sessions, ok := account[labelKey]
	if !ok {
		return nil, ErrNoRoute
	}

	var rss []ResolvedService

	for k, services := range sessions {
		for _, serv := range services {
			var rs ResolvedService

			rs.Agent = k
			rs.AgentKey = r.agentKeys[k]
			rs.ServiceId = serv.ServiceId.String()
			rs.ServiceType = serv.Type

			rss = append(rss, rs)
		}
	}

	return rss, nil
}
