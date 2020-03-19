package data

import (
	"bytes"
	"encoding/base32"
	"io"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/hashicorp/go-hclog"
	"go.etcd.io/bbolt"
	"golang.org/x/crypto/blake2b"
)

type Bolt struct {
	L  hclog.Logger
	db *bbolt.DB
}

func NewBolt(path string) (*Bolt, error) {
	opts := bbolt.DefaultOptions
	db, err := bbolt.Open(path, 0755, opts)
	if err != nil {
		return nil, err
	}

	b := &Bolt{
		L:  hclog.L().Named("bolt"),
		db: db,
	}

	return b, nil
}

func (b *Bolt) CertStorage() *CertStorage {
	return &CertStorage{b: b}
}

type CertStorage struct {
	b  *Bolt
	mu sync.Mutex
}

// Lock acquires the lock for key, blocking until the lock
// can be obtained or an error is returned. Note that, even
// after acquiring a lock, an idempotent operation may have
// already been performed by another process that acquired
// the lock before - so always check to make sure idempotent
// operations still need to be performed after acquiring the
// lock.
//
// The actual implementation of obtaining of a lock must be
// an atomic operation so that multiple Lock calls at the
// same time always results in only one caller receiving the
// lock at any given time.
//
// To prevent deadlocks, all implementations (where this concern
// is relevant) should put a reasonable expiration on the lock in
// case Unlock is unable to be called due to some sort of network
// failure or system crash.
func (c *CertStorage) Lock(key string) error {
	c.b.L.Debug("cert-storage lock", "key", key)
	c.mu.Lock()
	return nil
}

// Unlock releases the lock for key. This method must ONLY be
// called after a successful call to Lock, and only after the
// critical section is finished, even if it errored or timed
// out. Unlock cleans up any resources allocated during Lock.
func (c *CertStorage) Unlock(key string) error {
	c.b.L.Debug("cert-storage unlock", "key", key)
	c.mu.Unlock()
	return nil
}

// Store puts value at key.
func (c *CertStorage) Store(key string, value []byte) error {
	return c.b.db.Update(func(tx *bbolt.Tx) error {
		buk, err := tx.CreateBucketIfNotExists([]byte("certs"))
		if err != nil {
			return err
		}

		t := time.Now()

		data, err := t.MarshalBinary()
		if err != nil {
			return err
		}

		data = append(data, value...)

		c.b.L.Debug("cert-storage store", "key", key, "value-size", len(value), "value", hash(value))

		return buk.Put([]byte(key), data)
	})
}

func hash(value []byte) string {
	h, _ := blake2b.New256(nil)
	h.Write(value)

	return base32.HexEncoding.EncodeToString(h.Sum(nil))
}

// Load retrieves the value at key.
func (c *CertStorage) Load(key string) ([]byte, error) {
	var data []byte
	err := c.b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("certs"))
		if buk == nil {
			return certmagic.ErrNotExist(io.EOF)
		}

		data = buk.Get([]byte(key))

		if data == nil {
			return certmagic.ErrNotExist(io.EOF)
		}

		if data != nil {
			data = data[15:]
		}

		c.b.L.Debug("cert-storage load", "key", key, "value-size", len(data), "value", hash(data))
		return nil
	})

	if err != nil {
		return nil, err
	}

	return data, nil
}

// Delete deletes key.
func (c *CertStorage) Delete(key string) error {
	return c.b.db.Update(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("certs"))
		if buk == nil {
			return certmagic.ErrNotExist(io.EOF)
		}

		return buk.Delete([]byte(key))
	})
}

// Exists returns true if the key exists
// and there was no error checking.
func (c *CertStorage) Exists(key string) bool {
	var found bool

	c.b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("certs"))
		if buk == nil {
			return nil
		}

		found = buk.Get([]byte(key)) != nil
		return nil
	})

	return found
}

// List returns all keys that match prefix.
// If recursive is true, non-terminal keys
// will be enumerated (i.e. "directories"
// should be walked); otherwise, only keys
// prefixed exactly by prefix will be listed.
func (c *CertStorage) List(prefix string, recursive bool) ([]string, error) {
	var matches []string

	bprefix := []byte(prefix)
	bslash := []byte("/")

	err := c.b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("certs"))
		if buk == nil {
			return nil
		}

		return buk.ForEach(func(k, v []byte) error {
			if !recursive && bytes.Count(k, bslash) > 1 {
				return nil
			}

			if bytes.HasPrefix(k, bprefix) {
				matches = append(matches, string(v))
			}

			return nil
		})
	})

	c.b.L.Debug("cert-storage list", "prefix", prefix, "rec", recursive, "matches", matches)

	return matches, err
}

// Stat returns information about key.
func (c *CertStorage) Stat(key string) (certmagic.KeyInfo, error) {
	var ki certmagic.KeyInfo

	err := c.b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("certs"))
		if buk == nil {
			return nil
		}

		data := buk.Get([]byte(key))

		err := ki.Modified.UnmarshalBinary(data[:15])
		if err != nil {
			return err
		}

		ki.Size = int64(len(data) - 15)
		ki.IsTerminal = false

		return nil
	})

	if err != nil {
		return ki, err
	}

	return ki, nil
}
