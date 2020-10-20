package control

import (
	context "context"
	io "io"
	"sync"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/pkg/errors"
)

type inmemLockMgr struct {
	mu sync.Mutex

	locks  map[string]bool
	values map[string]string
}

var ErrLocked = errors.New("locked")

func (i *inmemLockMgr) GetLock(id, val string) (io.Closer, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.locks == nil {
		i.locks = make(map[string]bool)
	}

	if i.values == nil {
		i.values = make(map[string]string)
	}

	if i.locks[id] {
		return nil, ErrLocked
	}

	i.locks[id] = true
	i.values[id] = val

	return &inmemUnlock{i, id}, nil
}

func (i *inmemLockMgr) GetValue(id string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.values[id], nil
}

type inmemUnlock struct {
	i  *inmemLockMgr
	id string
}

func (i *inmemUnlock) Close() error {
	i.i.mu.Lock()
	defer i.i.mu.Unlock()

	i.i.locks[i.id] = false

	return nil
}

func NewConsulLockManager(ctx context.Context) (*consulLockMgr, error) {
	cfg := consul.DefaultConfig()
	client, err := consul.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	session := client.Session()

	id, _, err := session.CreateNoChecks(&consul.SessionEntry{
		Name:      "hzn",
		TTL:       "10s",
		LockDelay: 5 * time.Second,
	}, nil)

	if err != nil {
		return nil, err
	}

	go session.RenewPeriodic("5s", id, nil, ctx.Done())

	return &consulLockMgr{
		ctx:       ctx,
		client:    client,
		session:   id,
		localLock: make(map[string]bool),
	}, nil
}

type consulLockMgr struct {
	ctx     context.Context
	client  *consul.Client
	session string

	mu        sync.Mutex
	localLock map[string]bool
}

func (c *consulLockMgr) GetValue(id string) (string, error) {
	pair, _, err := c.client.KV().Get(id, nil)
	if err != nil {
		return "", err
	}

	if pair == nil {
		return "", nil
	}

	return string(pair.Value), nil
}

func (c *consulLockMgr) GetLock(id, val string) (io.Closer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.localLock[id] {
		return nil, ErrLocked
	}

	lock, err := c.client.LockOpts(&consul.LockOptions{
		Key:          id,
		Value:        []byte(val),
		Session:      c.session,
		LockTryOnce:  true,
		LockWaitTime: time.Second,
	})
	if err != nil {
		return nil, err
	}

	ch, err := lock.Lock(c.ctx.Done())
	if err != nil {
		return nil, err
	}

	if ch == nil {
		return nil, ErrLocked
	}

	c.localLock[id] = true

	return &consulUnlocker{c: c, id: id, lock: lock}, nil
}

type consulUnlocker struct {
	c    *consulLockMgr
	id   string
	lock *consul.Lock
}

func (c *consulUnlocker) Close() error {
	c.c.mu.Lock()
	defer c.c.mu.Unlock()

	c.c.localLock[c.id] = false

	return c.lock.Unlock()
}
