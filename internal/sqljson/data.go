package sqljson

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Data is a type that can be used as an attribute type in a GORM model to
// support storing arbitrary data in a record. The Data struct handles
// initialization of the underlying map internally.
//
// Data implements (database/sql/driver).Valuer and (database/sql).Scanner
// in order to be serializable into a SQL database. The encoding of the stored
// value is JSON so it can be stored in JSON column types.
//
// To support arbitrary values be encoded, Data uses json.RawMessage which
// allows the decoding into concrete types to be deferred until they are
// accessed. Use the Get and Set methods to access values.
type Data map[string]json.RawMessage

// Scan implements sql.Scanner and creates a Data instance with data from a
// database query result.
func (d *Data) Scan(src interface{}) error {
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf(
			"invalid database value of type %T, type assertion to []byte failed",
			src,
		)
	}

	return json.Unmarshal(b, &d)
}

// Value implements driver.Valuer and creates a value that can be stored in a
// database field from the Data instance.
func (d Data) Value() (driver.Value, error) {
	return json.Marshal(d)
}

// Get extracts the data value identified by the given key and decodes it
// into the passed out value. The type of the out value should be the same that
// was passed to Set when writing the value.
//
// The first return value indicates whether there is a data value with the
// given key. An error is returned as the second return value if decoding fails.
func (d Data) Get(key string, out interface{}) (bool, error) {
	v, ok := d[key]
	if !ok {
		return false, nil
	}

	return true, json.Unmarshal(v, out)
}

// Set stores the given key/value pair in the Data instance. If there
// already is a value with this key, it is overwritten.
// The value can be of any type that is JSON serializable and deserializable.
//
// Note that if the value includes dynamic types (e.g. a struct with an
// attribute with an interface type), the value should implement
// json.Unmarshaler.
//
// An error is returned if encoding the value fails.
func (d *Data) Set(key string, value interface{}) error {
	j, err := json.Marshal(value)
	if err != nil {
		return err
	}

	if *d == nil {
		*d = Data{}
	}

	(*d)[key] = j
	return nil
}
