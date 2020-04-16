package pb

import "time"

func (t *Timestamp) Time() time.Time {
	return time.Unix(int64(t.Sec), int64(t.Nsec))
}

func NewTimestamp(t time.Time) *Timestamp {
	return &Timestamp{Sec: uint64(t.Unix()), Nsec: uint64(t.Nanosecond())}
}

func (t *Timestamp) ToDuration() time.Duration {
	return (time.Second * time.Duration(t.Sec)) + time.Duration(t.Nsec)
}

func TimestampFromDuration(dur time.Duration) *Timestamp {
	return &Timestamp{
		Sec:  uint64(dur / time.Second),
		Nsec: uint64(dur % time.Second),
	}
}
