package workq

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry(t *testing.T) {
	t.Run("calls a handler func with the job value", func(t *testing.T) {

		type foo struct {
			Name string
			Age  int
		}

		f := func(jt string, f *foo) error {
			assert.Equal(t, jt, "foo_happened")
			assert.Equal(t, "boo", f.Name)
			assert.Equal(t, 42, f.Age)
			return nil
		}

		var r Registry

		r.Register("foo_happened", f)

		data, err := json.Marshal(&foo{Name: "boo", Age: 42})
		require.NoError(t, err)

		err = r.Handle(&Job{
			JobType: "foo_happened",
			Payload: data,
		})

		require.NoError(t, err)
	})
}
