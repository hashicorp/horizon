package wire

import (
	bytes "bytes"
	"encoding/json"

	"github.com/oklog/ulid"
)

type ULID struct {
	ulid.ULID
}

func (u ULID) Bytes() []byte {
	return u.ULID[:]
}

func (u ULID) Marshal() ([]byte, error) {
	return u.MarshalBinary()
}

func (u ULID) MarshalTo(data []byte) (n int, err error) {
	if len(u.ULID) == 0 {
		return 0, nil
	}
	copy(data, u.ULID[:])
	return 16, nil
}

func (u *ULID) Unmarshal(data []byte) error {
	if len(data) == 0 {
		u = nil
		return nil
	}

	copy(u.ULID[:], data)

	return nil
}

func (u *ULID) Size() int {
	if u == nil {
		return 0
	}
	if len(u.ULID) == 0 {
		return 0
	}
	return 16
}

func (u ULID) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.String())
}

func (u *ULID) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}

	u.ULID, err = ulid.Parse(s)
	return err
}

func (u ULID) Equal(other ULID) bool {
	return bytes.Equal(u.ULID[0:], other.ULID[0:])
}

func (u ULID) Compare(other ULID) int {
	return bytes.Compare(u.ULID[0:], other.ULID[0:])
}

func ULIDFromBytes(b []byte) ULID {
	var u ULID
	copy(u.ULID[:], b)
	return u
}
