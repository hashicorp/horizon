package registry

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"

	petname "github.com/dustinkirkland/golang-petname"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/data"
	"github.com/oklog/ulid"
	"golang.org/x/crypto/blake2b"
)

type Registry struct {
	key    []byte
	suffix string
	mu     sync.RWMutex

	sessions map[string]map[string]map[string]struct{}

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

const KeySize = blake2b.BlockSize

func NewRegistry(key []byte, suffix string, storage *data.Bolt) (*Registry, error) {
	reg := &Registry{
		suffix:   suffix,
		storage:  storage,
		sessions: make(map[string]map[string]map[string]struct{}),
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

func (r *Registry) verifyToken(L hclog.Logger, token string) (ulid.ULID, error) {
	L.Debug("verifying token", "token", token)

	var id ulid.ULID

	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return id, err
	}

	accId := data[:ulidSize]
	copy(id[:], accId)

	givenSum := data[ulidSize:]

	L.Debug("token account", "id", id.String(), "sum", givenSum)

	h, err := blake2b.New256(r.key)
	if err != nil {
		return id, err
	}

	h.Write(accId)

	sum := h.Sum(nil)

	if subtle.ConstantTimeCompare(sum, givenSum) == 0 {
		return id, ErrBadToken
	}

	return id, nil
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

func (r *Registry) AuthAgent(L hclog.Logger, token string, labels []string) (string, func(), error) {
	accId, err := r.FindAccount(L, token)
	if err != nil {
		return "", nil, err
	}

	id, err := ulid.New(ulid.Now(), mrand)
	if err != nil {
		return "", nil, err
	}

	agentKey := id.String()

	labelKey := compressLabels(labels)

	L.Info("agent authenticated", "id", id.String(), "label-key", labelKey, "account-id", accId.String())

	r.mu.Lock()
	defer r.mu.Unlock()

	account := r.sessions[accId.String()]
	if account == nil {
		account = make(map[string]map[string]struct{})
		r.sessions[accId.String()] = account
	}

	sessions := account[labelKey]
	if sessions == nil {
		sessions = make(map[string]struct{})
		account[labelKey] = sessions
	}

	sessions[agentKey] = struct{}{}

	remove := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(sessions, agentKey)
	}

	// Create a default route and tag it with the given labels

	created, err := r.storage.CreateDefaultRoute(accId.String(), labelKey)
	if err != nil {
		return "", nil, err
	}

	if created {
		L.Info("created default route", "account", accId.String(), "label-key", labelKey)
	}

	return agentKey, remove, nil
}

var (
	ErrNoAccount = errors.New("no account found")
	ErrNoRoute   = errors.New("no route found")
)

func (r *Registry) Token(L hclog.Logger, accId ulid.ULID) (string, error) {
	h, err := blake2b.New256(r.key)
	if err != nil {
		return "", err
	}

	h.Write(accId[:])

	sum := h.Sum(nil)

	token := append([]byte(nil), accId[:]...)
	token = append(token, sum...)

	L.Debug("created token", "account-id", accId.String(), "sum", sum)

	return base64.RawURLEncoding.EncodeToString(token), nil
}

func (r *Registry) FindAccount(L hclog.Logger, token string) (ulid.ULID, error) {
	accId, err := r.verifyToken(L, token)
	if err != nil {
		return ulid.ULID{}, err
	}

	if !r.storage.CheckAccount(accId.String()) {
		return ulid.ULID{}, ErrNoAccount
	}

	return accId, nil
}

func (r *Registry) ResolveAgent(L hclog.Logger, target string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	accId, labelKey, err := r.storage.LabelsForTarget(target)
	if err != nil {
		return "", err
	}

	if accId == "" || labelKey == "" {
		return "", ErrNoAccount
	}

	account, ok := r.sessions[accId]
	if !ok {
		L.Error("account missing", "id", accId)
		return "", ErrNoAccount
	}

	sessions, ok := account[labelKey]
	if !ok {
		return "", ErrNoRoute
	}

	var agentKey string

	for k, _ := range sessions {
		agentKey = k
		break
	}

	return agentKey, nil
}
