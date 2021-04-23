package control

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fakeCatalog struct {
	targets []string
}

func (f *fakeCatalog) Targets() []string {
	return f.targets
}

type fakeClient struct {
	addr     string
	services []*pb.AccountServices
	labels   []*pb.LabelLink
}

func (f *fakeClient) AddServices(ctx context.Context, in *pb.AccountServices, opts ...grpc.CallOption) (*pb.Noop, error) {
	f.services = append(f.services, in)
	return &pb.Noop{}, nil
}

func (f *fakeClient) AddLabeLink(ctx context.Context, in *pb.LabelLinks, opts ...grpc.CallOption) (*pb.Noop, error) {
	f.labels = append(f.labels, in.LabelLinks...)
	return &pb.Noop{}, nil
}

func TestBroadcaster(t *testing.T) {
	t.Run("fans out new account services to all hubs", func(t *testing.T) {
		var (
			fcat fakeCatalog
			fcli fakeClient
		)

		conn := func(addr string) (pb.HubServicesClient, error) {
			fcli.addr = addr

			return &fcli, nil
		}

		fcat.targets = []string{"1.2.3.4"}

		bc, err := NewBroadcaster(hclog.L(), &fcat, conn)
		require.NoError(t, err)

		as := &pb.AccountServices{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: pb.NewULID(),
			},
			Services: []*pb.ServiceRoute{
				{
					Hub: pb.NewULID(),
				},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = bc.AdvertiseServices(ctx, as)
		require.NoError(t, err)

		assert.Equal(t, "1.2.3.4", fcli.addr)
		require.Equal(t, 1, len(fcli.services))
		assert.Equal(t, as, fcli.services[0])
	})

	t.Run("fans out new label links to all hubs", func(t *testing.T) {
		var (
			fcat fakeCatalog
			fcli fakeClient
		)

		conn := func(addr string) (pb.HubServicesClient, error) {
			fcli.addr = addr

			return &fcli, nil
		}

		fcat.targets = []string{"1.2.3.4"}

		bc, err := NewBroadcaster(hclog.L(), &fcat, conn)
		require.NoError(t, err)

		labels := pb.ParseLabelSet(":hostname=foo")
		target := pb.ParseLabelSet("deployment=abc")

		ll := &pb.LabelLink{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: pb.NewULID(),
			},
			Labels: labels,
			Target: target,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = bc.AdvertiseLabelLinks(ctx, &pb.LabelLinks{
			LabelLinks: []*pb.LabelLink{ll},
		})
		require.NoError(t, err)

		assert.Equal(t, "1.2.3.4", fcli.addr)
		require.Equal(t, 1, len(fcli.labels))
		assert.Equal(t, ll, fcli.labels[0])

	})
}
