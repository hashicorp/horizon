package wire

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/pkg/errors"
)

type Context interface {
	Account() *pb.Account
	ReadMarshal(v Unmarshaller) (byte, error)
	WriteMarshal(tag byte, v Marshaller) error

	// Forwards any data between the 2 contexts
	BridgeTo(other Context) error

	// Returns a writer that will send traffic as framed messages
	Writer() io.WriteCloser

	// Returns a reader that recieves traffic as framed messages
	Reader() io.Reader

	// Returns the total number of messages and bytes, respectively, that the
	// context has transmitted.
	Accounting() (int64, int64)

	// Close the context and cleanup any resources. Does not close
	// any IOs though.
	Close() error
}

type ctx struct {
	accountId *pb.Account
	fr        *FramingReader
	fw        *FramingWriter

	// accounting
	messages *int64
	bytes    *int64

	closers []func() error
}

func NewContext(accountId *pb.Account, fr *FramingReader, fw *FramingWriter) Context {
	return &ctx{
		accountId: accountId,
		fr:        fr,
		fw:        fw,
		messages:  new(int64),
		bytes:     new(int64),
	}
}

func (c *ctx) Close() error {
	var err error

	for _, f := range c.closers {
		serr := f()
		if serr != nil {
			err = multierror.Append(err, serr)
		}
	}

	return err
}

type closeCtx struct {
	Context

	closers []func() error
}

func WithCloser(c Context, closers ...func() error) Context {
	if priv, ok := c.(*ctx); ok {
		priv.closers = append(priv.closers, closers...)
		return priv
	}

	return &closeCtx{Context: c, closers: closers}
}

func (cc *closeCtx) Close() error {
	var err error

	for _, f := range cc.closers {
		serr := f()
		if serr != nil {
			err = multierror.Append(err, serr)
		}
	}

	parent := cc.Context.Close()
	if parent != nil {
		err = multierror.Append(err, parent)
	}

	return err
}

func (c *ctx) Account() *pb.Account {
	return c.accountId
}

func (c *ctx) Accounting() (int64, int64) {
	return atomic.LoadInt64(c.messages), atomic.LoadInt64(c.bytes)
}

func (c *ctx) ReadMarshal(v Unmarshaller) (byte, error) {
	tag, _, err := c.fr.ReadMarshal(v)
	if err != nil {
		return 0, err
	}

	return tag, nil
}

func (c *ctx) WriteMarshal(tag byte, v Marshaller) error {
	sz, err := c.fw.WriteMarshal(tag, v)
	if err != nil {
		return err
	}

	atomic.AddInt64(c.messages, 1)
	atomic.AddInt64(c.bytes, int64(sz))
	return nil
}

func (c *ctx) Writer() io.WriteCloser {
	return c.fw.WriteAdapter()
}

func (c *ctx) Reader() io.Reader {
	return c.fr.ReadAdapter()
}

var ErrInvalidContext = errors.New("invalid context type")

func (c *ctx) copyTo(octx *ctx) error {
	buf := make([]byte, 32*1024)

	for {
		tag, sz, err := c.fr.Next()
		if err != nil {
			return err
		}

		err = octx.fw.WriteFrame(tag, sz)
		if err != nil {
			return err
		}

		_, err = io.CopyBuffer(octx.fw, c.fr, buf)
		if err != nil {
			return err
		}
	}
}

func (c *ctx) BridgeTo(other Context) error {
	octx, ok := other.(*ctx)
	if !ok {
		return ErrInvalidContext
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.copyTo(octx)
	}()

	octx.copyTo(c)

	wg.Wait()

	return nil
}
