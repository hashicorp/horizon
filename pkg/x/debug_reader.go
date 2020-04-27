package x

import (
	"io"

	"github.com/davecgh/go-spew/spew"
)

type DebugReader struct {
	R io.Reader
}

func (d DebugReader) Read(b []byte) (int, error) {
	n, err := d.R.Read(b)
	spew.Printf("read: %v, %v, %v\n", err, n, b[:n])
	return n, err
}

type DebugWriter struct {
	W io.Writer
}

func (d DebugWriter) Write(b []byte) (int, error) {
	n, err := d.W.Write(b)
	spew.Printf("write: %v, %v, %+v\n", err, n, b)
	return n, err
}
