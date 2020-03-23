package wire

import (
	"bufio"
	"io"

	"github.com/hashicorp/yamux"
)

type ComposedConn struct {
	*bufio.Reader
	io.Writer
	io.Closer
	Recyclable
}

func (c *ComposedConn) BufioReader() *bufio.Reader {
	return c.Reader
}

var _ yamux.BufioReaderer = &ComposedConn{}
