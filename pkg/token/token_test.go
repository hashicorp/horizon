package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToken(t *testing.T) {
	t.Run("create and validate a token", func(t *testing.T) {
		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}

		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		stoken, err := tc.EncodeED25519(key, "k1")
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

	t.Run("detect alterations", func(t *testing.T) {
		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}

		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		stoken, err := tc.EncodeED25519(key, "k1")
		require.NoError(t, err)

		token, err := RemoveArmor(stoken)
		require.NoError(t, err)

		var tkn pb.Token
		err = tkn.Unmarshal(token[1:])
		require.NoError(t, err)

		var body pb.Token_Body
		err = body.Unmarshal(tkn.Body)
		require.NoError(t, err)

		body.Capabilities = append(body.Capabilities, pb.TokenCapability{
			Capability: pb.ACCESS,
			Value:      "/",
		})

		data, err := body.Marshal()
		require.NoError(t, err)

		tkn.Body = data

		evilToken, err := tkn.Marshal()
		require.NoError(t, err)

		stoken = Armor(append(token[:1], evilToken...))

		_, err = CheckTokenED25519(stoken, pub)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrBadToken))
	})

	t.Run("detect wrong key", func(t *testing.T) {
		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}

		_, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		stoken, err := tc.EncodeED25519(key, "k1")
		require.NoError(t, err)

		pub, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		_, err = CheckTokenED25519(stoken, pub)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrBadToken))
	})

	t.Run("checks the tokens is before the end of the time window", func(t *testing.T) {
		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}
		tc.ValidDuration = time.Second

		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		stoken, err := tc.EncodeED25519(key, "k1")
		require.NoError(t, err)

		time.Sleep(time.Second)

		_, err = CheckTokenED25519(stoken, pub)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNoLongerValid))
	})

	t.Run("checks the tokens is after the beginning of the time window", func(t *testing.T) {
		n := timeNow
		defer func() {
			timeNow = n
		}()

		timeNow = func() time.Time {
			return time.Now().Add(-10 * time.Minute)
		}

		var tc TokenCreator
		tc.AccountId = pb.NewULID()
		tc.AccuntNamespace = "/test"
		tc.Capabilities = map[pb.Capability]string{
			pb.CONNECT: "",
			pb.SERVE:   "",
		}

		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		stoken, err := tc.EncodeED25519(key, "k1")
		require.NoError(t, err)

		time.Sleep(time.Second)

		_, err = CheckTokenED25519(stoken, pub)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNoLongerValid))
	})

}
