package control

import (
	"bytes"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var zwriters = sync.Pool{
	New: func() interface{} {
		w, _ := zstd.NewWriter(nil)
		return w
	},
}

func zstdCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer

	w := zwriters.Get().(*zstd.Encoder)
	w.Reset(&buf)

	defer zwriters.Put(w)

	defer w.Close()

	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func zstdDecompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	r, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	if _, err := io.Copy(&buf, r); err != nil {
		return nil, err
	}
	r.Close()

	return buf.Bytes(), nil
}
