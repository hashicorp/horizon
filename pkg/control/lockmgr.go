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
	mu   sync.Mutex
	cond *sync.Cond

	locks  map[string]bool
	values map[string]string
}

var ErrLocked = errors.New("locked")

func (i *inmemLockMgr) GetLock(id, val string) (io.Closer, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.cond == nil {
		i.cond = sync.NewCond(&i.mu)
	}

	if i.locks == nil {
		i.locks = make(map[string]bool)
	}

	if i.values == nil {
		i.values = make(map[string]string)
	}

	for {
		if i.locks[id] {
			i.cond.Wait()
		} else {
			break
		}
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
	i.i.cond.Broadcast()

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

	lm := &consulLockMgr{
		ctx:       ctx,
		client:    client,
		session:   id,
		localLock: make(map[string]bool),
	}

	lm.cond = sync.NewCond(&lm.mu)

	return lm, nil
}

type consulLockMgr struct {
	ctx     context.Context
	client  *consul.Client
	session string

	mu        sync.Mutex
	cond      *sync.Cond
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

	for {
		if c.localLock[id] {
			c.cond.Wait()
		} else {
			break
		}
	}

	lock, err := c.client.LockOpts(&consul.LockOptions{
		Key:          id,
		Value:        []byte(val),
		Session:      c.session,
		LockWaitTime: time.Second,
	})
	if err != nil {
		return nil, err
	}

	c.mu.Unlock()
	ch, err := lock.Lock(c.ctx.Done())
	c.mu.Lock()

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

	delete(c.c.localLock, c.id)
	c.c.cond.Broadcast()

	return c.lock.Unlock()
}
