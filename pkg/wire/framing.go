package wire

import (
	"bufio"
	"encoding/binary"
	"io"
	"sync"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/pkg/errors"
)

var frPool = sync.Pool{
	New: func() interface{} { return &FramingReader{} },
}

type FramingReader struct {
	br *bufio.Reader

	readLeft int
}

func NewFramingReader(r io.Reader) (*FramingReader, error) {
	vf := frPool.Get().(*FramingReader)

	if vf.br == nil {
		vf.br = bufio.NewReader(r)
	} else {
		vf.br.Reset(r)
	}

	return vf, nil
}

func (f *FramingReader) Recycle() {
	// frPool.Put(f)
}

func (f *FramingReader) BufReader() *bufio.Reader {
	return f.br
}

type recycleFR struct {
	io.Reader
	f *FramingReader
}

func (r *recycleFR) Recycle() {
	r.f.Recycle()
}

func (f *FramingReader) RecylableBufReader() io.Reader {
	return &recycleFR{Reader: f.br, f: f}
}

type Recyclable interface {
	Recycle()
}

func Recycle(v interface{}) {
	if r, ok := v.(Recyclable); ok {
		r.Recycle()
	}
}

func (f *FramingReader) Next() (byte, int, error) {
	tag, err := f.br.ReadByte()
	if err != nil {
		return 0, 0, err
	}

	sz, err := binary.ReadUvarint(f.br)
	if err != nil {
		return 0, 0, err
	}

	f.readLeft = int(sz)

	return tag, int(sz), nil
}

func (f *FramingReader) Read(b []byte) (int, error) {
	if f.readLeft == 0 {
		return 0, io.EOF
	}

	if f.readLeft > len(b) {
		f.readLeft -= len(b)
	} else {
		b = b[:f.readLeft]
		f.readLeft = 0
	}

	return f.br.Read(b)
}

var frameBufPool = sync.Pool{}

type Unmarshaller interface {
	Unmarshal([]byte) error
}

var ErrRemoteError = errors.New("remote error detected")

func (f *FramingReader) ReadMarshal(v Unmarshaller) (byte, int, error) {
	tag, sz, err := f.Next()
	if err != nil {
		return 0, 0, err
	}

	buf, ok := frameBufPool.Get().([]byte)
	if !ok || sz > len(buf) {
		buf = make([]byte, sz+128)
	}

	defer frameBufPool.Put(buf)

	_, err = io.ReadFull(f, buf[:sz])
	if err != nil {
		return 0, 0, err
	}

	if tag == 255 {
		var resp pb.Response
		resp.Unmarshal(buf[:sz])
		return 0, 0, errors.Wrapf(ErrRemoteError, resp.Error)
	}

	err = v.Unmarshal(buf[:sz])
	return tag, sz, err
}

func (f *FramingReader) ReadAdapter() *ReadAdapter {
	return &ReadAdapter{FR: f}
}

var fwPool = sync.Pool{
	New: func() interface{} {
		return &FramingWriter{
			sz: make([]byte, binary.MaxVarintLen64),
		}
	},
}

type flusher interface {
	Flush() error
}

type FramingWriter struct {
	bw    *bufio.Writer
	flush flusher

	sz        []byte
	writeLeft int
}

func NewFramingWriter(w io.Writer) (*FramingWriter, error) {
	fw := fwPool.Get().(*FramingWriter)

	if fw.bw == nil {
		fw.bw = bufio.NewWriter(w)
	} else {
		fw.bw.Reset(w)
	}

	if f, ok := w.(flusher); ok {
		fw.flush = f
	} else {
		fw.flush = nil
	}

	return fw, nil
}

func (f *FramingWriter) Recycle() {
	// fwPool.Put(f)
}

func (f *FramingWriter) WriteFrame(tag byte, size int) error {
	bufsz := binary.PutUvarint(f.sz, uint64(size))

	b := f.sz[:bufsz]

	f.bw.WriteByte(tag)
	f.bw.Write(b)

	if size == 0 {
		f.bw.Flush()
		if f.flush != nil {
			f.flush.Flush()
		}
	} else {
		f.writeLeft = size
	}

	return nil
}

var ErrTooMuchData = errors.New("more data that expected passed in")

func (f *FramingWriter) Write(b []byte) (int, error) {
	if f.writeLeft == 0 {
		return 0, io.EOF
	}

	if len(b) > f.writeLeft {
		return 0, ErrTooMuchData
	}

	n, err := f.bw.Write(b)
	if err != nil {
		return n, err
	}

	f.writeLeft -= n

	if f.writeLeft == 0 {
		err = f.bw.Flush()
		if f.flush != nil {
			f.flush.Flush()
		}
	}

	return n, err
}

type Marshaller interface {
	Size() int
	MarshalTo(b []byte) (int, error)
}

func (f *FramingWriter) WriteMarshal(tag byte, v Marshaller) (int, error) {
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

	err = f.WriteFrame(tag, sz)
	if err != nil {
		return 0, err
	}

	return f.Write(buf[:sz])
}

func (f *FramingWriter) WriteAdapter() *WriteAdapter {
	return &WriteAdapter{FW: f}
}
