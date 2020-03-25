package agent

import (
	"sync"

	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

type logTransmitter struct {
	mu      sync.Mutex
	path    string
	session *yamux.Session
	stream  *yamux.Stream
	fw      *wire.FramingWriter
}

func (l *logTransmitter) transmit(msg *logs.Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var wreq wire.Request
	wreq.Method = "POST"
	wreq.Path = l.path
	wreq.Host = "logs.edge"

	if l.fw == nil {
		stream, err := l.session.OpenStream()
		if err != nil {
			return err
		}

		fw, err := wire.NewFramingWriter(stream)
		if err != nil {
			return err
		}

		_, err = fw.WriteMarshal(1, &wreq)
		if err != nil {
			return err
		}

		l.stream = stream
		l.fw = fw
	}

	_, err := l.fw.WriteMarshal(logs.MesgTag, msg)
	return err
}
