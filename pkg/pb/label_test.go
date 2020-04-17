package pb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLabelSet(t *testing.T) {
	t.Run("can match a subset", func(t *testing.T) {
		ls := ParseLabelSet("service=www,env=prod,instance=aabbcc")

		target := ParseLabelSet("service=www,env=prod")

		require.True(t, target.Matches(ls))
		require.False(t, ls.Matches(target))
	})
}

