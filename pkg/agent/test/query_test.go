package agent_test

import (
	"context"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/data"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/registry"
	"github.com/stretchr/testify/require"
	"gotest.tools/assert"
)

func TestQuery(t *testing.T) {
	t.Run("allows an agent to query another agent in the same account", func(t *testing.T) {
		dir, err := ioutil.TempDir("", "agent")
		require.NoError(t, err)

		defer os.RemoveAll(dir)

		l, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer l.Close()

		db, err := data.NewBolt(filepath.Join(dir, "db.db"))
		require.NoError(t, err)

		L := hclog.L()
		reg, err := registry.NewRegistry(registry.RandomKey(), ".localhost", db)

		acc, err := reg.AddAccount(L)
		require.NoError(t, err)

		token, err := reg.Token(L, acc)
		require.NoError(t, err)

		dkey, err := noiseconn.GenerateKey()
		require.NoError(t, err)

		h, err := hub.NewHub(L.Named("hub"), reg, dkey)
		require.NoError(t, err)

		h.AddDefaultServices()

		addr := l.Addr().(*net.TCPAddr)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go h.Serve(ctx, l)

		gkey1, err := noiseconn.GenerateKey()
		require.NoError(t, err)

		g1, err := agent.NewAgent(L.Named("g1"), gkey1)
		require.NoError(t, err)

		g1.Token = token

		sid, err := g1.AddService(&agent.Service{
			Type:        "echo",
			Labels:      []string{"env=test1"},
			Description: "a test echo service",
			Handler:     agent.EchoHandler(),
		})

		require.NoError(t, err)

		g1.Start(ctx, []agent.HubConfig{
			{
				Addr:      addr.String(),
				PublicKey: noiseconn.PublicKey(dkey),
			},
		})

		gkey2, err := noiseconn.GenerateKey()
		require.NoError(t, err)

		g2, err := agent.NewAgent(L.Named("g2"), gkey2)
		require.NoError(t, err)

		g2.Token = token

		g2.Start(ctx, []agent.HubConfig{
			{
				Addr:      addr.String(),
				PublicKey: noiseconn.PublicKey(dkey),
			},
		})

		services, err := g2.QueryPeerService([]string{"env=test1"})
		require.NoError(t, err)

		require.Equal(t, 1, len(services), "no services found")

		serv := services[0]

		assert.Equal(t, sid, serv.ServiceId)
		assert.Equal(t, "echo", serv.Type)
		assert.Equal(t, noiseconn.PublicKey(gkey1), serv.AgentKey)
	})
}
