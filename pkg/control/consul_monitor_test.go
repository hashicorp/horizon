package control_test

import (
	"context"
	"testing"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsulMonitor(t *testing.T) {
	L := hclog.L()

	t.Run("can track hubs", func(t *testing.T) {
		cfg := consul.DefaultConfig()
		cm, err := control.NewConsulMonitor(L, cfg)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go cm.Watch(ctx)

		ch, err := hub.NewConsulHealth("a", cfg, "1.2.3.4")
		require.NoError(t, err)

		defer ch.DeregisterService(context.Background())

		err = ch.Start(ctx, L)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		addr, ok := cm.Lookup("a")
		assert.True(t, ok)

		assert.Equal(t, "1.2.3.4", addr)
	})
}
