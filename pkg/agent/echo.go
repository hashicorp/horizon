package agent

import (
	"context"
	"io"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
)

type echoHandler struct{}

func (_ *echoHandler) HandleRequest(ctx context.Context, L hclog.Logger, sctx ServiceContext) error {
	var mb wire.MarshalBytes

	for {
		tag, err := sctx.ReadMarshal(&mb)
		if err != nil {
			if err == io.EOF {
				return nil
			}

			return err
		}

		sctx.WriteMarshal(tag, &mb)
	}
}

func EchoHandler() ServiceHandler {
	return &echoHandler{}
}
