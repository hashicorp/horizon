package agent

import (
	"context"
	"io"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
)

type echoHandler struct{}

func (_ *echoHandler) HandleRequest(ctx context.Context, L hclog.Logger, stream *yamux.Stream, fr *wire.FramingReader, fw *wire.FramingWriter, req *wire.Request, ltrans *logTransmitter) error {

	for {
		tag, sz, err := fr.Next()
		if err != nil {
			return err
		}

		fw.WriteFrame(tag, sz)

		io.Copy(fw, fr)
	}
}

func EchoHandler() ServiceHandler {
	return &echoHandler{}
}
