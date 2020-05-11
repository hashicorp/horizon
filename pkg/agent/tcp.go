package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
)

type tcpHandler struct {
	addr string
}

func TCPHandler(addr string) ServiceHandler {
	return &tcpHandler{addr}
}

func (h *tcpHandler) HandleRequest(ctx context.Context, L hclog.Logger, sctx ServiceContext) error {
	defer sctx.Close()
	L.Info("tcp handler started")
	proto := sctx.ProtocolId()

	if !(proto == "" || proto == "tcp") {
		return fmt.Errorf("unknown protocol: %s", proto)
	}

	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		return err
	}

	id := pb.NewULID()

	L.Info("tcp session started", "id", id, "addr", h.addr, "session-addr", c.LocalAddr())

	r := sctx.Reader()
	w := sctx.Writer()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer w.Close()
		L.Info("read closed")

		io.Copy(w, c)
	}()

	go func() {
		defer wg.Done()
		defer c.Close()

		io.Copy(c, r)
	}()

	wg.Wait()

	L.Info("tcp session ended", "id", id)

	return nil
}
