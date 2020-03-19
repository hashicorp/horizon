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
	"github.com/oklog/ulid"
	"github.com/y0ssar1an/q"
	"golang.org/x/crypto/blake2b"
)

type Registry struct {
	key           []byte
	suffix        string
	mu            sync.RWMutex
	accounts      map[string]*Account
	targetAccount map[string]string
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

func NewRegistry(key []byte, suffix string) (*Registry, error) {
	reg := &Registry{
		suffix:        suffix,
		accounts:      make(map[string]*Account),
		targetAccount: make(map[string]string),
	}

	petname.NonDeterministicMode()

	return reg, nil
}

const ulidSize = 16

var ErrBadToken = errors.New("bad token detected")

func (r *Registry) verifyToken(L hclog.Logger, token string) (ulid.ULID, error) {
	var id ulid.ULID

	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return id, err
	}

	accId := data[:ulidSize]
	copy(id[:], accId)

	givenSum := data[ulidSize:]

	q.Q(data, accId, givenSum)

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

func (r *Registry) AddAccount(L hclog.Logger) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	defTarget := petname.Generate(3, "-") + r.suffix

	id, err := ulid.New(ulid.Now(), mrand)
	if err != nil {
		return nil, err
	}

	acc := &Account{
		id:        id,
		defTarget: defTarget,
		sessions:  make(map[string]map[string]struct{}),
		routes:    make(map[string]*Route),
	}

	r.accounts[id.String()] = acc
	r.targetAccount[defTarget] = id.String()

	L.Info("created account", "id", id.String(), "def-target", acc.defTarget)

	return acc, nil
}

func compressLabels(v []string) string {
	sort.Strings(v)

	return strings.ToLower(strings.Join(v, ", "))
}

func (r *Registry) AuthAgent(L hclog.Logger, token string, labels []string) (string, func(), error) {
	acc, err := r.FindAccount(L, token)
	if err != nil {
		return "", nil, err
	}

	id, err := ulid.New(ulid.Now(), mrand)
	if err != nil {
		return "", nil, err
	}

	agentKey := id.String()

	labelKey := compressLabels(labels)

	acc.mu.Lock()
	defer acc.mu.Unlock()

	L.Info("agent authenticated", "id", id.String(), "label-key", labelKey, "account-id", acc.id.String())

	sessions := acc.sessions[labelKey]
	if sessions == nil {
		sessions = make(map[string]struct{})
		acc.sessions[labelKey] = sessions
	}

	sessions[agentKey] = struct{}{}

	remove := func() {
		acc.mu.Lock()
		defer acc.mu.Unlock()
		delete(sessions, agentKey)
	}

	// Create a default route and tag it with the given labels
	if len(acc.routes) == 0 {
		L.Info("creating default route", "target", acc.defTarget, "label-key", labelKey)

		acc.routes[acc.defTarget] = &Route{
			target:   acc.defTarget,
			labelKey: labelKey,
		}
	}

	return agentKey, remove, nil
}

var (
	ErrNoAccount = errors.New("no account found")
	ErrNoRoute   = errors.New("no route found")
)

type Route struct {
	target   string
	labelKey string
}

type Account struct {
	id        ulid.ULID
	defTarget string
	mu        sync.RWMutex
	sessions  map[string]map[string]struct{}
	routes    map[string]*Route
}

func (r *Registry) Token(L hclog.Logger, acc *Account) (string, error) {
	h, err := blake2b.New256(r.key)
	if err != nil {
		return "", err
	}

	h.Write(acc.id[:])

	sum := h.Sum(nil)

	token := append([]byte(nil), acc.id[:]...)
	token = append(token, sum...)

	L.Debug("created token", "account-id", acc.id.String(), "sum", sum)

	q.Q(acc.id, sum, token)

	return base64.RawURLEncoding.EncodeToString(token), nil
}

func (r *Registry) FindAccount(L hclog.Logger, token string) (*Account, error) {
	accId, err := r.verifyToken(L, token)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	acc, ok := r.accounts[accId.String()]
	if !ok {
		return nil, ErrNoAccount
	}

	return acc, nil
}

func (r *Registry) ResolveAgent(L hclog.Logger, target string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	accId, ok := r.targetAccount[target]
	if !ok {
		L.Info("no account for target", "target", target)
		return "", ErrNoAccount
	}

	acc, ok := r.accounts[accId]
	if !ok {
		L.Error("account missing", "id", accId)
		return "", ErrNoAccount
	}

	acc.mu.RLock()
	defer acc.mu.RUnlock()

	route, ok := acc.routes[target]
	if !ok {
		return "", ErrNoRoute
	}

	sessions, ok := acc.sessions[route.labelKey]
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
