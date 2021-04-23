package hub

import (
	"context"

	"github.com/davecgh/go-spew/spew"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/pkg/errors"
	"google.golang.org/grpc/metadata"
)

// InboundServer is exposed as a gRPC server to allow alterations to be made to the recent
// data settings. It is called from the control server running in the central tier.
type InboundServer struct {
	Client *control.Client
}

var ErrBadControlToken = errors.New("bad control token")

func (i *InboundServer) checkValidToken(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ErrBadControlToken
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return ErrBadControlToken
	}

	token, err := token.CheckTokenED25519(auth[0], i.Client.TokenPub())
	if err != nil {
		return err
	}

	if token.Body.Role != pb.CONTROL {
		return errors.Wrapf(ErrBadControlToken, "role was: %s", token.Body.Role)
	}

	return nil
}

// AddServices adds the given services to the recent services list. It checks that the request comes
// with a valid token with the role of CONTROL.
func (i *InboundServer) AddServices(ctx context.Context, services *pb.AccountServices) (*pb.Noop, error) {
	err := i.checkValidToken(ctx)
	if err != nil {
		spew.Dump(err)
		return nil, err
	}

	err = i.Client.AddRecentAccountServices(services)
	return &pb.Noop{}, err
}

// AddLabelLink adds the given label links to a recent list. It checks that the request comes
// with a valid token with the role of CONTROL.
func (i *InboundServer) AddLabeLink(ctx context.Context, labels *pb.LabelLinks) (*pb.Noop, error) {
	err := i.checkValidToken(ctx)
	if err != nil {
		return nil, err
	}

	err = i.Client.AddRecentLabelLinks(labels)
	return &pb.Noop{}, nil
}
