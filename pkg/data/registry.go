package data

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/hashicorp/horizon/pkg/labels"
	"go.etcd.io/bbolt"
)

func (b *Bolt) Empty() bool {
	var empty bool

	b.db.Update(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("accounts"))
		empty = buk == nil
		return nil
	})

	return empty
}

func (b *Bolt) HandlingHostname(name string) bool {
	var ok bool

	b.db.View(func(tx *bbolt.Tx) error {
		targets := tx.Bucket([]byte("account-targets"))
		if targets == nil {
			return nil
		}

		ok = targets.Get([]byte(name)) != nil
		return nil
	})

	return ok
}

type labelLink struct {
	Account string
	Target  []string
}

func (b *Bolt) FindLabelLink(source []string) (string, []string, error) {
	labelKey := labels.CompressLabels(source)

	var ll labelLink

	b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("global-labels"))
		if buk == nil {
			return nil
		}

		data := buk.Get([]byte(labelKey))
		if data == nil {
			return nil
		}

		return json.Unmarshal(data, &ll)
	})

	return ll.Account, ll.Target, nil
}

func (b *Bolt) AddLabelLink(account string, source, target []string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		buk, err := tx.CreateBucketIfNotExists([]byte("global-labels"))
		if err != nil {
			return err
		}

		data, err := json.Marshal(labelLink{
			Account: account,
			Target:  target,
		})

		if err != nil {
			return err
		}

		return buk.Put([]byte(labels.CompressLabels(source)), data)
	})
}

func (b *Bolt) AddAccount(id, target string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		buk, err := tx.CreateBucketIfNotExists([]byte("accounts"))
		if err != nil {
			return err
		}

		err = buk.Put([]byte(id), []byte(target))
		if err != nil {
			return err
		}

		targets, err := tx.CreateBucketIfNotExists([]byte("account-targets"))
		if err != nil {
			return err
		}

		return targets.Put([]byte(target), []byte(id))
	})
}

func (b *Bolt) KnownTarget(name string) bool {
	var ok bool

	b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("account-targets"))
		if buk == nil {
			return nil
		}

		ok = buk.Get([]byte(name)) != nil
		return nil
	})

	return ok
}

func (b *Bolt) CheckAccount(id string) bool {
	var ok bool

	b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("accounts"))
		if buk == nil {
			return nil
		}

		ok = buk.Get([]byte(id)) != nil
		return nil
	})

	return ok
}

func (b *Bolt) AddRoute(target, labelKey string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		buk, err := tx.CreateBucketIfNotExists([]byte("routes"))
		if err != nil {
			return err
		}

		return buk.Put([]byte(target), []byte(labelKey))
	})
}

func (b *Bolt) LabelsForTarget(target string) (string, string, error) {
	var (
		labelKey string
		accId    string
	)

	err := b.db.View(func(tx *bbolt.Tx) error {
		routes := tx.Bucket([]byte("routes"))
		if routes == nil {
			return nil
		}

		data := routes.Get([]byte(target))
		if data != nil {
			labelKey = string(data)
		}

		targets := tx.Bucket([]byte("account-targets"))
		if targets == nil {
			return nil
		}

		data = targets.Get([]byte(target))
		if data != nil {
			accId = string(data)
		}

		return nil
	})

	if err != nil {
		return "", "", err
	}

	return accId, labelKey, nil
}

func (b *Bolt) AddService(accId, labelKey, serviceId, serviceType string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		ar, err := tx.CreateBucketIfNotExists([]byte("account-services"))
		if err != nil {
			return err
		}

		buk, err := ar.CreateBucketIfNotExists([]byte(accId))
		if err != nil {
			return err
		}

		id := serviceType + ":" + serviceId

		return buk.Put([]byte(labelKey), []byte(id))
	})
}

func (b *Bolt) LookupService(accId, labelKey string) (string, string, error) {
	var id string

	err := b.db.View(func(tx *bbolt.Tx) error {
		ar, err := tx.CreateBucketIfNotExists([]byte("account-services"))
		if err != nil {
			return err
		}

		buk, err := ar.CreateBucketIfNotExists([]byte(accId))
		if err != nil {
			return err
		}

		id = string(buk.Get([]byte(labelKey)))
		return nil
	})

	if err != nil {
		return "", "", err
	}

	if id != "" {
		colon := strings.IndexByte(id, ':')
		if colon != -1 {
			return id[:colon], id[colon+1:], nil
		}
	}

	return "", "", nil
}

func (b *Bolt) CreateDefaultRoute(accId, labelKey string) (bool, error) {
	var created bool

	return created, b.db.Update(func(tx *bbolt.Tx) error {
		ar, err := tx.CreateBucketIfNotExists([]byte("account-routes"))
		if err != nil {
			return err
		}

		buk, err := ar.CreateBucket([]byte(accId))
		if err != nil {
			if err == bbolt.ErrBucketExists {
				return nil
			}

			return err
		}

		created = true

		targets := tx.Bucket([]byte("accounts"))
		if targets == nil {
			return io.EOF
		}

		target := targets.Get([]byte(accId))

		err = buk.Put(target, []byte(labelKey))
		if err != nil {
			return err
		}

		buk, err = tx.CreateBucketIfNotExists([]byte("routes"))
		if err != nil {
			return err
		}

		return buk.Put([]byte(target), []byte(labelKey))
	})

}
