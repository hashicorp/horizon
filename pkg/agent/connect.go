package agent

import (
	"io"
	"net"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/pierrec/lz4/v3"
	"github.com/pkg/errors"
)

type Conn struct {
	io.Reader
	io.WriteCloser

	Stream *yamux.Stream
	Labels *pb.LabelSet
}

type agentConnAddr struct {
	labels *pb.LabelSet
}

func (c *Conn) Close() error {
	c.WriteCloser.Close()
	return c.Stream.Close()
}

func (a *agentConnAddr) Network() string {
	return "hzn"
}

func (a *agentConnAddr) String() string {
	if a.labels == nil {
		return "type=local"
	}

	return a.labels.SpecString()
}

// LocalAddr returns the local network address.
func (c *Conn) LocalAddr() net.Addr {
	return &agentConnAddr{}
}

// RemoteAddr returns the remote network address.
func (c *Conn) RemoteAddr() net.Addr {
	return &agentConnAddr{labels: c.Labels}
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.Stream.SetDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.Stream.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.Stream.SetWriteDeadline(t)
}

func (a *Agent) Connect(labels *pb.LabelSet) (net.Conn, error) {
	a.mu.Lock()
	stream, err := a.sessions[0].OpenStream()
	a.mu.Unlock()

	if err != nil {
		return nil, errors.Wrapf(err, "error opening new yamux stream")
	}

	sr := lz4.NewReader(stream)
	sw := lz4.NewWriter(stream)

	fw, err := wire.NewFramingWriter(sw)
	if err != nil {
		return nil, err
	}

	fr, err := wire.NewFramingReader(sr)
	if err != nil {
		return nil, err
	}

	var conreq pb.ConnectRequest
	conreq.Target = labels

	_, err = fw.WriteMarshal(1, &conreq)
	if err != nil {
		return nil, errors.Wrapf(err, "error writing connect request")
	}

	var ack pb.ConnectAck

	tag, _, err := fr.ReadMarshal(&ack)
	if err != nil {
		return nil, err
	}

	if tag != 1 {
		return nil, wire.ErrProtocolError
	}

	ctx := wire.NewContext(nil, fr, fw)

	r := ctx.Reader()
	w := ctx.Writer()

	return &Conn{Reader: r, WriteCloser: w, Stream: stream}, nil
}
