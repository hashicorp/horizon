package token

import (
	"context"

	"google.golang.org/grpc/credentials"
)

// Token implements the credentials provider interface to provide the
// given string token in the way that Horizon expects authentication.
type Token string

func (t Token) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": string(t),
	}, nil
}

func (t Token) RequireTransportSecurity() bool {
	return false
}

var _ credentials.PerRPCCredentials = Token("")
