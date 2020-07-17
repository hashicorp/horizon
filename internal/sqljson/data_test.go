package sqljson

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test that Scan returns an error if an invalid value is given.
func TestData_Scan_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
	}{{
		// String slices cannot be converted to []byte which is why this should
		// fail.
		name:  "incompatible type",
		value: []string{"foo", "bar"},
	}, {
		// Only JSON encoded values can be scanned. Strings need to be quoted
		// to be valid JSON.
		name:  "invalid JSON",
		value: []byte("foo"),
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			m := Data{}

			r.Error(m.Scan(tc.value))
		})
	}
}

// Test that Scan populates a Data instance correctly.
func TestData_Scan_Success(t *testing.T) {
	r := require.New(t)

	m := Data{}

	r.NoError(m.Scan([]byte(`{"foo": "bar"}`)))

	var v string
	ok, err := m.Get("foo", &v)
	r.True(ok)
	r.NoError(err)

	r.Equal("bar", v)
}

// Test that Value creates a JSON representation of the Data instance.
func TestData_Value_Success(t *testing.T) {
	r := require.New(t)

	m := Data{}
	r.NoError(m.Set("foo", "bar"))

	val, err := m.Value()
	r.NoError(err)
	r.IsType([]byte{}, val)

	var res map[string]string
	r.NoError(json.Unmarshal(val.([]byte), &res))
	r.Equal("bar", res["foo"])
}

// Test that Get returns false but no error if a key does not exist. This should
// be true if the Data itself is nil or if the Data is non-nil but does
// not contain the requested key.
func TestData_Get_NotFound(t *testing.T) {
	cases := []struct {
		name string
		m    Data
	}{{
		name: "data is nil",
		m:    nil,
	}, {
		name: "data is empty",
		m:    Data{},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			var v interface{}
			ok, err := tc.m.Get("foo", &v)

			r.NoError(err)
			r.False(ok)
		})
	}
}

// Test that Get returns an error if invalid JSON is found in a Data value.
func TestData_Get_Invalid(t *testing.T) {
	r := require.New(t)

	// create a Data instance with an invalid value. Note that JSON encoded
	// strings are quoted, thus `bar` is not valid.
	m := Data{
		"foo": []byte("bar"),
	}

	var v string
	ok, err := m.Get("foo", &v)

	r.Error(err)
	r.True(ok)
	r.Empty(v)
}

// Test that Get finds a value that was stored using Set previously.
func TestData_SetGet_Success(t *testing.T) {
	cases := []struct {
		name string
		m    Data
	}{{
		name: "data is nil",
		m:    nil,
	}, {
		name: "data is empty",
		m:    Data{},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			r.NoError(tc.m.Set("foo", "bar"))

			var v string
			ok, err := tc.m.Get("foo", &v)

			r.NoError(err)
			r.True(ok)
			r.Equal("bar", v)
		})
	}
}

// Test that Set returns an error if the value cannot be JSON encoded.
func TestData_Set_Invalid(t *testing.T) {
	r := require.New(t)

	m := Data{}

	// Try to store a channel in Data. Channels cannot be JSON encoded which
	// is why this should fail.
	err := m.Set("foo", make(chan interface{}))

	r.IsType(&json.UnsupportedTypeError{}, err)
}
