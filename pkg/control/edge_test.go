package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestEdge(t *testing.T) {
	L := hclog.L()

	t.Run("finds services by label in an account", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db.Debug()

		accId := pb.NewULID()

		var ao Account
		ao.ID = accId.Bytes()
		ao.Namespace = "/test"

		err := dbx.Check(db.Save(&ao))
		require.NoError(t, err)

		ll := pb.ParseLabelSet("env=dev,service=www,depid=1,myextra=foo")

		hubId := pb.NewULID()

		servId := pb.NewULID()

		account := &pb.Account{
			Namespace: "/test",
			AccountId: accId,
		}

		var service Service
		service.ServiceId = servId.Bytes()
		service.AccountId = account.Key()
		service.Labels = ll.AsStringArray()
		service.HubId = hubId.Bytes()
		service.Type = "test"

		err = dbx.Check(db.Save(&service))
		require.NoError(t, err)

		var other Service
		serv2 := pb.NewULID()
		other.ServiceId = serv2.Bytes()
		other.AccountId = account.Key()
		other.Labels = pb.ParseLabelSet("env=prod,service=www,depid=1,myextra=foo").AsStringArray()
		other.HubId = hubId.Bytes()
		other.Type = "test"

		err = dbx.Check(db.Save(&other))
		require.NoError(t, err)

		a2 := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/test",
		}

		serv3 := pb.NewULID()

		var o2 Service
		o2.ServiceId = serv3.Bytes()
		o2.AccountId = a2.Key()
		o2.Labels = ll.AsStringArray()
		o2.HubId = hubId.Bytes()
		o2.Type = "test"

		err = dbx.Check(db.Save(&o2))
		require.NoError(t, err)

		ctx := context.Background()

		resp, err := s.queryDBServices(ctx, &pb.LookupEndpointsRequest{
			Account: account,
			Labels:  pb.ParseLabelSet("env=dev,service=www"),
		})
		require.NoError(t, err)

		require.Len(t, resp.Routes, 1)

		route := resp.Routes[0]

		assert.Equal(t, hubId, route.Hub)
		assert.Equal(t, service.Type, route.Type)
		assert.Equal(t, route.Id, pb.ULIDFromBytes(service.ServiceId))
	})

	t.Run("finds labellinks", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db.Debug()

		accId := pb.NewULID()

		account := &pb.Account{
			Namespace: "/test",
			AccountId: accId,
		}

		var ao Account
		ao.ID = account.Key()
		ao.Namespace = "/test"

		var pblimits pb.Account_Limits
		pblimits.Bandwidth = 4.5
		pblimits.HttpRequests = 30.3
		ao.Data.Set("limits", &pblimits)

		err := dbx.Check(db.Save(&ao))
		require.NoError(t, err)

		ll := pb.ParseLabelSet("host=foo.bar")
		tgt := pb.ParseLabelSet("env=prod,service=www")

		var link LabelLink
		link.Account = &ao
		link.AccountID = account.Key()
		link.Labels = FlattenLabels(ll)
		link.Target = FlattenLabels(tgt)

		err = dbx.Check(db.Save(&link))
		require.NoError(t, err)

		ctx := context.Background()

		resp, err := s.queryDBLabelLinks(ctx, &pb.ResolveLabelLinkRequest{
			Labels: ll,
		})

		require.NoError(t, err)

		assert.Equal(t, account, resp.Account)
		assert.Equal(t, tgt, resp.Labels)
		assert.Equal(t, &pblimits, resp.Limits)
	})
}

func TestEdgeClient(t *testing.T) {
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	scfg := ServerConfig{
		KeyId:             "k1",
		RegisterToken:     "aabbcc",
		DisablePrometheus: true,
		SigningKey:        privKey,
	}

	t.Run("can lookup a service", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)
		pb.RegisterEdgeServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		id := pb.NewULID()

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			GRPCConn: gcc,
		})

		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: account,
			Id:      serviceId,
			Type:    "test",
			Labels:  labels,
			Metadata: []*pb.KVPair{
				{
					Key:   "version",
					Value: "0.1x",
				},
			},
		}

		err = client.AddService(ctx, servReq)
		require.NoError(t, err)

		calc, err := client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services := calc.Services()

		assert.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)

		hubId2 := pb.NewULID()
		serviceId2 := pb.NewULID()
		serviceId3 := pb.NewULID()

		// Nuke to force an inline refresh
		delete(client.accountServices, account.SpecString())

		hubtoken, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		md3 := make(metadata.MD)
		md3.Set("authorization", hubtoken.Token)

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     pb.NewULID(),
				Id:      serviceId2,
				Type:    "test",
				Labels:  labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		)
		require.NoError(t, err)

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId2,
				Id:      serviceId3,
				Type:    "test",
				Labels:  labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		)
		require.NoError(t, err)

		calc, err = client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services = calc.All

		require.Equal(t, 3, len(services))

		assert.Equal(t, serviceId, services[0].Id)
		assert.Equal(t, serviceId2, services[1].Id)
		assert.Equal(t, serviceId3, services[2].Id)

		var notId int

		for i := 0; i < 20; i++ {
			serv := calc.Services()

			assert.Equal(t, serviceId, serv[0].Id, "local service is always first")

			if !calc.Services()[1].Id.Equal(serviceId2) {
				notId++
			}
		}

		assert.True(t, notId > 2)
		assert.True(t, notId < 18)
	})

	t.Run("resolves label links", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)
		pb.RegisterEdgeServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		id := pb.NewULID()

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			GRPCConn: gcc,
		})

		require.NoError(t, err)

		label := pb.ParseLabelSet(":hostname=foo.com")
		target := pb.ParseLabelSet("service=www,env=prod")

		_, err = s.AddAccount(
			metadata.NewIncomingContext(top, md2),
			&pb.AddAccountRequest{
				Account: account,
			},
		)

		require.NoError(t, err)

		_, err = s.AddLabelLink(
			metadata.NewIncomingContext(top, md2),
			&pb.AddLabelLinkRequest{
				Labels:  label,
				Account: account,
				Target:  target,
			},
		)

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		labelAccount, labelTarget, _, err := client.ResolveLabelLink(label)
		require.NoError(t, err)

		assert.Equal(t, account, labelAccount)
		assert.Equal(t, target, labelTarget)
	})

}
