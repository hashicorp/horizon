// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package wire

import (
	"bufio"
	"io"
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

// var _ yamux.BufioReaderer = &ComposedConn{}
