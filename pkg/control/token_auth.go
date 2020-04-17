package control

import (
	context "context"
)

type Token string

func (t Token) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": string(t),
	}, nil
}

func (t Token) RequireTransportSecurity() bool {
	return false
}
