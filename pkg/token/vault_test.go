package token

import (
	"testing"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVault(t *testing.T) {
	vc := testutils.SetupVault()

	t.Run("can setup a key and use it", func(t *testing.T) {
		id := pb.NewULID().SpecString()

		pub, err := SetupVault(vc, id)
		require.NoError(t, err)

		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}

		stoken, err := tc.EncodeED25519WithVault(vc, id, "k1")
		require.NoError(t, err)

		vt, err := CheckTokenED25519(stoken, pub)
		require.NoError(t, err)

		cb := func(ok bool, _ string) bool {
			return ok
		}

		assert.True(t, cb(vt.HasCapability(pb.CONNECT)))
		assert.True(t, cb(vt.HasCapability(pb.SERVE)))
		assert.False(t, cb(vt.HasCapability(pb.ACCESS)))
		assert.Equal(t, "k1", vt.KeyId)
	})
}
