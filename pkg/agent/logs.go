package agent

import (
	"fmt"
	"sync"

	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

type LogTransmitter struct {
	mu     sync.Mutex
	path   string
	stream *yamux.Stream
	fw     *wire.FramingWriter

	session *yamux.Session
	agent   *Agent
}

func (l *LogTransmitter) Transmit(msg *logs.Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.session == nil {
		l.agent.mu.RLock()

		l.session = l.agent.sessions[0]
		if l.session == nil {
			return fmt.Errorf("no connected sessions")
		}

		l.agent.mu.RUnlock()
	}

	var wreq pb.Request
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
