package data

import "go.etcd.io/bbolt"

func (b *Bolt) GetConfig(key string) ([]byte, error) {
	var data []byte
	err := b.db.View(func(tx *bbolt.Tx) error {
		buk := tx.Bucket([]byte("config"))
		if buk == nil {
			return nil
		}

		data = buk.Get([]byte(key))
		return nil
	})

	return data, err
}

func (b *Bolt) SetConfig(key string, val []byte) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		buk, err := tx.CreateBucketIfNotExists([]byte("config"))
		if err != nil {
			return err
		}

		return buk.Put([]byte(key), val)
	})
}
