package connect

import (
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

type Session struct {
	conn    net.Conn
	session *yamux.Session
}

type Conn struct {
	serviceId *pb.ULID
	fr        *wire.FramingReader
	fw        *wire.FramingWriter
}

var ErrInvalidToken = errors.New("invalid token")

// We're trying this because in a busy system it could be a limiting
// factor so having it pre-tracked will be useful.
// TODO(emp): expose this via expvar or something like that.
var activeSessions = new(int64)

func Connect(L hclog.Logger, addr, token string) (*Session, error) {
	var clientTlsConfig tls.Config
	clientTlsConfig.InsecureSkipVerify = true
	clientTlsConfig.NextProtos = []string{"hzn"}

	cconn, err := tls.Dial("tcp", addr, &clientTlsConfig)
	if err != nil {
		return nil, err
	}

	var preamble pb.Preamble
	preamble.Token = token

	fw, err := wire.NewFramingWriter(cconn)
	if err != nil {
		return nil, err
	}

	_, err = fw.WriteMarshal(1, &preamble)
	if err != nil {
		return nil, err
	}

	fr, err := wire.NewFramingReader(cconn)
	if err != nil {
		return nil, err
	}

	var confirmation pb.Confirmation

	_, _, err = fr.ReadMarshal(&confirmation)
	if err != nil {
		return nil, err
	}

	if confirmation.Status != "connected" {
		return nil, ErrInvalidToken
	}

	bc := &wire.ComposedConn{
		Reader: fr.BufReader(),
		Writer: cconn,
		Closer: cconn,
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})
	cfg.LogOutput = nil

	session, err := yamux.Client(bc, cfg)
	if err != nil {
		return nil, err
	}

	val := atomic.AddInt64(activeSessions, 1)
	metrics.SetGauge([]string{"connect", "sessions"}, float32(val))

	return &Session{conn: cconn, session: session}, nil
}

func (s *Session) Close() error {
	val := atomic.AddInt64(activeSessions, -1)
	metrics.SetGauge([]string{"connect", "sessions"}, float32(val))

	s.session.Close()
	return s.conn.Close()
}

func (s *Session) ConnecToAccountService(acc *pb.Account, labels *pb.LabelSet) (*Conn, error) {
	stream, err := s.session.OpenStream()
	if err != nil {
		return nil, err
	}

	fr2, err := wire.NewFramingReader(stream)
	if err != nil {
		return nil, err
	}

	fw2, err := wire.NewFramingWriter(stream)
	if err != nil {
		return nil, err
	}

	var conreq pb.ConnectRequest
	conreq.Target = labels
	conreq.PivotAccount = acc

	_, err = fw2.WriteMarshal(1, &conreq)
	if err != nil {
		return nil, err
	}

	var ack pb.ConnectAck

	tag, _, err := fr2.ReadMarshal(&ack)
	if err != nil {
		return nil, err
	}

	if tag != 1 {
		return nil, wire.ErrProtocolError
	}

	return &Conn{serviceId: ack.ServiceId, fr: fr2, fw: fw2}, nil
}

func (s *Session) ConnecToService(labels *pb.LabelSet) (*Conn, error) {
	return s.ConnecToAccountService(nil, labels)
}

func (c *Conn) ReadMarshal(v wire.Unmarshaller) (byte, error) {
	tag, _, err := c.fr.ReadMarshal(v)
	if err != nil {
		return 0, err
	}

	return tag, nil
}

func (c *Conn) WriteMarshal(tag byte, v wire.Marshaller) error {
	_, err := c.fw.WriteMarshal(tag, v)
	return err
}

func (c *Conn) WireContext(accountId *pb.Account) wire.Context {
	return wire.NewContext(accountId, c.fr, c.fw)
}

func (c *Conn) ServiceId() *pb.ULID {
	return c.serviceId
}
