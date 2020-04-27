// Copyright 2019 LINE Corporation
//
// LINE Corporation licenses this file to you under the Apache License,
// version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at:
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package lz4

import (
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"sync/atomic"

	"github.com/pierrec/lz4/v3"
	"google.golang.org/grpc/encoding"
)

// Name is the name registered for the gzip compressor.
const Name = "lz4"

const (
	// DefaultCompressionLevel default compression level (0=fastest)
	DefaultCompressionLevel = 0

	// BestCompressionLevel best but slowest compression level
	BestCompressionLevel = 16
)

func init() {
	c := &compressor{}
	c.poolCompressor.New = func() interface{} {
		return &writer{Writer: lz4.NewWriter(ioutil.Discard), pool: &c.poolCompressor}
	}
	encoding.RegisterCompressor(c)
}

var compressionLevel int32

type writer struct {
	*lz4.Writer
	pool *sync.Pool
}

// SetLevel thread-safe sets compression level.
func SetLevel(level int) error {
	if level < DefaultCompressionLevel || level > BestCompressionLevel {
		return fmt.Errorf("grpc: invalid gzip compression level: %d", level)
	}
	atomic.StoreInt32(&compressionLevel, int32(level))
	return nil
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	z := c.poolCompressor.Get().(*writer)
	z.Writer.CompressionLevel = int(atomic.LoadInt32(&compressionLevel))
	z.Writer.Reset(w)
	return z, nil
}

func (z *writer) Close() (err error) {
	err = z.Writer.Close()
	z.pool.Put(z)
	return
}

type reader struct {
	*lz4.Reader
	pool *sync.Pool
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	z, inPool := c.poolDecompressor.Get().(*reader)
	if inPool {
		z.Reset(r)
		return z, nil
	}
	return &reader{Reader: lz4.NewReader(r), pool: &c.poolDecompressor}, nil
}

func (z *reader) Read(p []byte) (n int, err error) {
	if n, err = z.Reader.Read(p); err == io.EOF {
		z.pool.Put(z)
	}

	return
}

func (c *compressor) Name() string {
	return Name
}

type compressor struct {
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}
