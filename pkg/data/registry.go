package data

import (
	"io"

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
