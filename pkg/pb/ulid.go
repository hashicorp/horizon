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

func (u ULID) Bytes() []byte {
	x := u.native()
	return x[:]
}
