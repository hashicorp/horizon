package ctxlog

import (
	"context"

	"github.com/hashicorp/go-hclog"
)

type ctxLogKey struct{}

func L(ctx context.Context) hclog.Logger {
	logger := ctx.Value(ctxLogKey{})
	if logger == nil {
		return hclog.L()
	}
	return logger.(hclog.Logger)
}

func Inject(ctx context.Context, log hclog.Logger) context.Context {
	return context.WithValue(ctx, ctxLogKey{}, log)
}
