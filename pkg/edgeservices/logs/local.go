package logs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/mozillazg/go-slugify"
	"github.com/pkg/errors"
)

const Backlog = 25

const MesgTag = 1

var ErrProtocolError = errors.New("protocol error")

type LocalService struct {
	Dir string

	mu    sync.Mutex
	sinks map[string]chan *Message
}

func (l *LocalService) logPath(reqPath string) string {
	return filepath.Join(l.Dir, slugify.Slugify(reqPath))
}

func (l *LocalService) HandleRequest(ctx context.Context, L hclog.Logger, wctx wire.Context, req *wire.Request) error {

	path := l.logPath(req.Path)

	l.mu.Lock()

	c, ok := l.sinks[path]
	if !ok {
		c = make(chan *Message, Backlog)
		go l.sinkLogs(ctx, L, path, c)
	}

	l.mu.Unlock()

	for {
		var msg Message

		tag, err := wctx.ReadRequest(&msg)
		if err != nil {
			return err
		}

		L.Trace("ingested log", "tag", tag)

		if tag == 0 {
			return nil
		}

		if tag != MesgTag {
			return errors.Wrapf(ErrProtocolError, "incorrect tag: %d", tag)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()

		case c <- &msg:
			// ok
		}
	}
}

func (l *LocalService) sinkLogs(ctx context.Context, L hclog.Logger, path string, ch chan *Message) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		L.Error("error opening log file", "error", err, "path", path)
		return
	}

	jw := json.NewEncoder(f)

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			jw.Encode(msg)
		}
	}
}
