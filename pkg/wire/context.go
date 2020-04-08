package wire

import (
	"io"
	"sync"

	"github.com/pkg/errors"
)

type Context interface {
	AccountId() string
	ReadMarshal(v Unmarshaller) (byte, error)
	WriteMarshal(tag byte, v Marshaller) error

	// Forwards any data between the 2 contexts
	BridgeTo(other Context) error

	// Returns a writer that will send traffic as framed messages
	Writer() io.WriteCloser

	// Returns a reader that recieves traffic as framed messages
	Reader() io.Reader
}

type ctx struct {
	accountId string
	fr        *FramingReader
	fw        *FramingWriter
}

func NewContext(accountId string, fr *FramingReader, fw *FramingWriter) Context {
	return &ctx{
		accountId: accountId,
		fr:        fr,
		fw:        fw,
	}
}

func (c *ctx) AccountId() string {
	return c.accountId
}

func (c *ctx) ReadMarshal(v Unmarshaller) (byte, error) {
	tag, _, err := c.fr.ReadMarshal(v)
	if err != nil {
		return 0, err
	}

	return tag, nil
}

func (c *ctx) WriteMarshal(tag byte, v Marshaller) error {
	_, err := c.fw.WriteMarshal(tag, v)
	return err
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
