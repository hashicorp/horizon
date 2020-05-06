package pb

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid"
)

var mrand = ulid.Monotonic(rand.Reader, 1)

func NewULID() *ULID {
	id := ulid.MustNew(ulid.Now(), mrand)

	return &ULID{
		Timestamp: id.Time(),
		Entropy:   id.Entropy(),
	}
}

func ULIDFromBytes(b []byte) *ULID {
	var id ulid.ULID
	copy(id[:], b)

	return &ULID{
		Timestamp: id.Time(),
		Entropy:   id.Entropy(),
	}
}

func ParseULID(s string) (*ULID, error) {
	id, err := ulid.Parse(s)
	if err != nil {
		return nil, err
	}

	return &ULID{
		Timestamp: id.Time(),
		Entropy:   id.Entropy(),
	}, nil
}

// Used when generating internal tokens for use by hub services
var InternalAccount *ULID

func init() {
	InternalAccount, _ = ParseULID("01E7KKMY4HKNZWATXC6SDQ1V4D")
}

func (u *ULID) Time() time.Time {
	return ulid.Time(u.Timestamp)
}

func (u *ULID) native() ulid.ULID {
	var ux ulid.ULID

	ux.SetTime(u.Timestamp)
	copy(ux[6:], u.Entropy)

	return ux
}

func (u *ULID) SpecString() string {
	return u.native().String()
}

func (u *ULID) String() string {
	return u.SpecString()
}

func (u ULID) Bytes() []byte {
	x := u.native()
	return x[:]
}
