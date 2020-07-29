package pb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelSet(t *testing.T) {
	t.Run("can match a subset", func(t *testing.T) {
		ls := ParseLabelSet("service=www,env=prod,instance=aabbcc")

		target := ParseLabelSet("service=www,env=prod")

		require.True(t, target.Matches(ls))
		require.False(t, ls.Matches(target))
	})

	t.Run("uses case folding on match", func(t *testing.T) {
		ls := ParseLabelSet("service=www,env=prod,instance=aabbcc")

		target := ParseLabelSet("service=www,env=prod")

		target.Labels[1].Name = "ServICE"
		target.Labels[1].Value = "WWW"

		require.True(t, target.Matches(ls))
		require.False(t, ls.Matches(target))
	})

	t.Run("is stabley sorted", func(t *testing.T) {
		ls := ParseLabelSet("service=emp,env=test")
		assert.Equal(t, "env", ls.Labels[0].Name)

		ls.Finalize()

		assert.Equal(t, "env", ls.Labels[0].Name)

		ls.Finalize()

		assert.Equal(t, "env", ls.Labels[0].Name)
	})

	t.Run("sorts by name then value", func(t *testing.T) {
		ls := ParseLabelSet("service=emp,env=test")
		assert.Equal(t, "env", ls.Labels[0].Name)

		ls = ParseLabelSet("a=x,b=c")
		assert.Equal(t, "a", ls.Labels[0].Name)

		ls = ParseLabelSet("service=foo,service=emp")
		assert.Equal(t, "foo", ls.Labels[1].Value)
	})

	t.Run("lowercases on parsing", func(t *testing.T) {
		ls := ParseLabelSet("service=emp,ENV=test")
		assert.Equal(t, "env", ls.Labels[0].Name)

		ls.Finalize()

		assert.Equal(t, "env", ls.Labels[0].Name)

		ls.Finalize()

		assert.Equal(t, "env", ls.Labels[0].Name)
	})

}
