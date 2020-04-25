package wire

import (
	"io"

	"github.com/pkg/errors"
)

const (
	adaptTagEOF  = 240
	adaptTagData = 241
)

type WriteAdapter struct {
	FW *FramingWriter
}

func (f *WriteAdapter) Write(b []byte) (int, error) {
	err := f.FW.WriteFrame(adaptTagData, len(b))
	if err != nil {
		return 0, err
	}

	return f.FW.Write(b)
}

func (f *WriteAdapter) Close() error {
	return f.FW.WriteFrame(adaptTagEOF, 0)
}

type ReadAdapter struct {
	FR     *FramingReader
	rest   int
	closed bool
}

var ErrProtocolError = errors.New("protocol error detected")

func (f *ReadAdapter) Read(b []byte) (int, error) {
	if f.closed {
		return 0, io.EOF
	}

	if f.rest > 0 {
		if f.rest < len(b) {
			n := f.rest
			f.rest = 0
			return io.ReadFull(f.FR, b[:n])
		} else {
			n := len(b)
			f.rest -= n
			return io.ReadFull(f.FR, b)
		}
	}

	tag, sz, err := f.FR.Next()
	if err != nil {
		return 0, err
	}

	if tag == adaptTagEOF {
		f.closed = true
		return 0, io.EOF
	}

	if tag != adaptTagData {
		return 0, errors.Wrapf(ErrProtocolError, "wrong tag detected: %d (wanted %d)", tag, adaptTagData)
	}

	if sz < len(b) {
		return io.ReadFull(f.FR, b[:sz])
	}

	n, err := io.ReadFull(f.FR, b)
	if err != nil {
		return n, err
	}

	f.rest = sz - n

	return n, nil
}
