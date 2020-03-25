package logs

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/edgeservices"
	"github.com/hashicorp/horizon/pkg/wire"
)

func Now() *Timestamp {
	t := time.Now()

	return &Timestamp{
		Sec:  uint64(t.Unix()),
		Nsec: uint32(t.Nanosecond()),
	}
}

type LogService struct {
}

func (l *LogService) HandleRequest(ctx context.Context, L hclog.Logger, ca edgeservices.FrameAccessor, req *wire.Request) error {
	panic("not implemented")
}
