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
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuery(t *testing.T) {
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
		Labels:      []agent.Label{{Name: "env", Value: "test1"}},
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

	t.Run("allows an agent to query another agent in the same account", func(t *testing.T) {
		services, err := g2.QueryPeerService([]string{"env=test1"})
		require.NoError(t, err)

		require.Equal(t, 1, len(services), "no services found")

		serv := services[0]

		assert.Equal(t, sid, serv.ServiceId)
		assert.Equal(t, "echo", serv.Type)
		assert.Equal(t, noiseconn.PublicKey(gkey1), serv.AgentKey)
	})

	t.Run("allows an agent to connect to another agents service", func(t *testing.T) {
		services, err := g2.QueryPeerService([]string{"env=test1"})
		require.NoError(t, err)

		require.Equal(t, 1, len(services), "no services found")

		serv := services[0]

		wctx, err := g2.ConnectToPeer(serv)
		require.NoError(t, err)

		ts := wire.Now()

		err = wctx.WriteMarshal(1, ts)
		require.NoError(t, err)

		var ts2 wire.Timestamp

		tag, err := wctx.ReadMarshal(&ts2)
		require.NoError(t, err)

		assert.Equal(t, byte(1), tag)

		assert.True(t, ts.Equal(&ts2), "timestamps did notecho properly")
	})
}
