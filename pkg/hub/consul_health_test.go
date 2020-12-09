package hub

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

func TestConsulHealth(t *testing.T) {
	t.Run("maintains a proper view of the hub catalog", func(t *testing.T) {
		cfg := consul.DefaultConfig()
		a, err := NewConsulHealth("a", cfg)
		require.NoError(t, err)

		b, err := NewConsulHealth("b", cfg)
		require.NoError(t, err)

		parent, cancel := context.WithCancel(context.Background())
		defer cancel()

		achk, err := a.registerService(parent)
		require.NoError(t, err)

		defer a.deregisterService(parent)

		err = a.passCheck(achk)
		require.NoError(t, err)

		bchk, err := b.registerService(parent)
		require.NoError(t, err)

		defer b.deregisterService(parent)

		err = b.passCheck(bchk)
		require.NoError(t, err)

		_, err = a.refreshHubs(parent, 0, time.Second)
		require.NoError(t, err)

		_, err = b.refreshHubs(parent, 0, time.Second)
		require.NoError(t, err)

		a.mu.Lock()

		var hubs []string

		for k := range a.hubs {
			hubs = append(hubs, k)
		}

		sort.Strings(hubs)

		a.mu.Unlock()

		assert.Equal(t, []string{"a", "b"}, hubs)

		assert.True(t, a.Available("b"))

		err = b.deregisterService(parent)
		require.NoError(t, err)

		time.Sleep(time.Second)

		_, err = a.refreshHubs(parent, 0, time.Second)
		require.NoError(t, err)

		a.mu.Lock()

		hubs = nil

		for k := range a.hubs {
			hubs = append(hubs, k)
		}

		a.mu.Unlock()

		assert.False(t, a.Available("b"))
		assert.Equal(t, []string{"a"}, hubs)
	})

	t.Run("notices service changes without polling", func(t *testing.T) {
		cfg := consul.DefaultConfig()
		a, err := NewConsulHealth("a", cfg)
		require.NoError(t, err)

		b, err := NewConsulHealth("b", cfg)
		require.NoError(t, err)

		parent, cancel := context.WithCancel(context.Background())
		defer cancel()

		achk, err := a.registerService(parent)
		require.NoError(t, err)

		defer a.deregisterService(parent)

		err = a.passCheck(achk)
		require.NoError(t, err)

		bchk, err := b.registerService(parent)
		require.NoError(t, err)

		defer b.deregisterService(parent)

		err = b.passCheck(bchk)
		require.NoError(t, err)

		go a.Watch(parent, 10*time.Second)

		time.Sleep(time.Second)

		assert.True(t, a.Available("b"))

		err = b.deregisterService(parent)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		assert.False(t, a.Available("b"))
	})

	t.Run("has a comfortable default mode", func(t *testing.T) {
		cfg := consul.DefaultConfig()
		a, err := NewConsulHealth("a", cfg)
		require.NoError(t, err)

		b, err := NewConsulHealth("b", cfg)
		require.NoError(t, err)

		parent, cancel := context.WithCancel(context.Background())
		defer cancel()

		bctx, bcancel := context.WithCancel(parent)

		err = a.Start(parent, hclog.L())
		require.NoError(t, err)

		err = b.Start(bctx, hclog.L())
		require.NoError(t, err)

		time.Sleep(time.Second)

		assert.True(t, a.Available("b"))

		t.Log("waiting past the check TTL to make sure they stay alive")

		dur, err := time.ParseDuration(CheckTTL)
		require.NoError(t, err)

		time.Sleep(dur + (1 * time.Second))

		assert.True(t, a.Available("b"))

		t.Log("canceling b, watching it fall")

		bcancel()

		time.Sleep(100 * time.Millisecond)

		assert.False(t, a.Available("b"))
	})
}
