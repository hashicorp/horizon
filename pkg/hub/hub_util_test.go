package hub

import (
	"testing"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubUtil(t *testing.T) {
	mkLocs := func(addrs ...string) []*pb.NetworkLocation {
		var out []*pb.NetworkLocation

		for _, addr := range addrs {
			var loc pb.NetworkLocation
			loc.Addresses = []string{addr}
			out = append(out, &loc)
		}

		return out
	}

	t.Run("can pick when there is only one choise", func(t *testing.T) {
		var h Hub

		addr, err := h.pickAddress(mkLocs("127.0.0.1"))
		require.NoError(t, err)

		assert.Equal(t, "127.0.0.1", addr)
	})

	t.Run("can pick from an addr with the same labels", func(t *testing.T) {
		var h Hub

		h.location = mkLocs("1.1.1.1", "10.0.1.1")
		h.location[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		tgt := mkLocs("2.2.2.2", "10.0.1.2")
		tgt[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		addr, err := h.pickAddress(tgt)
		require.NoError(t, err)

		assert.Equal(t, "10.0.1.2", addr)
	})

	t.Run("considers private addrs before public ones", func(t *testing.T) {
		var h Hub

		h.location = mkLocs("1.1.1.1", "10.0.1.1")
		h.location[0].Labels = pb.ParseLabelSet("type=public,dc=test")
		h.location[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		tgt := mkLocs("2.2.2.2", "10.0.1.2")
		tgt[0].Labels = pb.ParseLabelSet("type=public,dc=test")
		tgt[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		addr, err := h.pickAddress(tgt)
		require.NoError(t, err)

		assert.Equal(t, "10.0.1.2", addr)
	})

	t.Run("uses a public one if there are no private", func(t *testing.T) {
		var h Hub

		h.location = mkLocs("1.1.1.1", "10.0.1.1")
		h.location[0].Labels = pb.ParseLabelSet("type=public,dc=test")
		h.location[1].Labels = pb.ParseLabelSet("type=private,dc=test2")

		tgt := mkLocs("2.2.2.2", "10.0.1.2")
		tgt[0].Labels = pb.ParseLabelSet("type=public,dc=test")
		tgt[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		addr, err := h.pickAddress(tgt)
		require.NoError(t, err)

		assert.Equal(t, "2.2.2.2", addr)
	})

	t.Run("uses a public one when there are no matches", func(t *testing.T) {
		var h Hub

		h.location = mkLocs("1.1.1.1", "10.0.1.1")
		h.location[0].Labels = pb.ParseLabelSet("type=public,dc=test2")
		h.location[1].Labels = pb.ParseLabelSet("type=private,dc=test2")

		tgt := mkLocs("2.2.2.2", "10.0.1.2")
		tgt[0].Labels = pb.ParseLabelSet("type=public,dc=test")
		tgt[1].Labels = pb.ParseLabelSet("type=private,dc=test")

		addr, err := h.pickAddress(tgt)
		require.NoError(t, err)

		assert.Equal(t, "2.2.2.2", addr)
	})
}
