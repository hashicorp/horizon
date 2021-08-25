package control

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

		var ao Account
		ao.ID = accId.Bytes()
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
		link.Labels = FlattenLabels(ll)
		link.Target = FlattenLabels(tgt)

		account := &pb.Account{
			Namespace: "/test",
			AccountId: accId,
		}

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
