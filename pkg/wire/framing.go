package wire

import (
	"encoding/binary"
	io "io"
	"sync"
)

var frameBufPool = sync.Pool{}

const minBufSize = 1024

type Framing struct {
	RW    io.ReadWriter
	szbuf [4]byte
}

type Unmarshaller interface {
	Unmarshal([]byte) error
}

func (f *Framing) ReadMessage(v Unmarshaller) (int, error) {
	_, err := io.ReadFull(f.RW, f.szbuf[:])
	if err != nil {
		return 0, err
	}

	sz := int(binary.BigEndian.Uint32(f.szbuf[:]))

	buf, ok := frameBufPool.Get().([]byte)
	if !ok || sz > len(buf) {
		buf = make([]byte, sz+128)
	}

	defer frameBufPool.Put(buf)

	_, err = io.ReadFull(f.RW, buf[:sz])
	if err != nil {
		return 4, err
	}

	err = v.Unmarshal(buf[:sz])
	if err != nil {
		return 4 + sz, err
	}

	return 4 + sz, nil
}

type Marshaller interface {
	Size() int
	MarshalTo([]byte) (int, error)
}

func (f *Framing) WriteMessage(v Marshaller) (int, error) {
	sz := v.Size()
	buf, ok := frameBufPool.Get().([]byte)
	if !ok || sz > len(buf) {
		buf = make([]byte, sz+128)
	}

	defer frameBufPool.Put(buf)

	_, err := v.MarshalTo(buf[:sz])
	if err != nil {
		return 0, err
	}

	binary.BigEndian.PutUint32(f.szbuf[:], uint32(sz))

	n, err := f.RW.Write(f.szbuf[:])
	if err != nil {
		return n, err
	}

	m, err := f.RW.Write(buf[:sz])
	if err != nil {
		return n + m, err
	}

	return n + m, nil
}
