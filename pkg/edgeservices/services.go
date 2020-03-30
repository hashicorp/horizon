package edgeservices

import (
	"context"
	"io"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
)

type FrameAccessor interface {
	io.Reader
	io.Writer
	Next() (byte, int, error)
	WriteFrame(tag byte, sz int) error
	WriteResponse(resp *wire.Response) error
	ReadMarshal(v wire.Unmarshaller) (byte, int, error)
	WriteMarshal(v wire.Marshaller) (int, error)
}

type ServiceHandler interface {
	HandleRequest(ctx context.Context, L hclog.Logger, wctx wire.Context, req *wire.Request) error
}

type Service struct {
	Host    string
	Handler ServiceHandler
}

type Services struct {
	mu         sync.RWMutex
	registered map[string]Service
}

func (s *Services) Register(serv Service) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.registered == nil {
		s.registered = map[string]Service{}
	}

	s.registered[serv.Host] = serv
}

func (s *Services) Lookup(host string) (Service, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	serv, ok := s.registered[host]
	return serv, ok
}
