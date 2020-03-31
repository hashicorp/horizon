package wire

import (
	"time"
)

func Now() *Timestamp {
	t := time.Now()

	return &Timestamp{
		Sec:  uint64(t.Unix()),
		Nsec: uint64(t.Nanosecond()),
	}
}
