package wire

import (
	bytes "bytes"
	"io"
	"io/ioutil"
	"testing"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/pierrec/lz4/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFraming(t *testing.T) {
	t.Run("handles running over lz4 properly", func(t *testing.T) {
		var out bytes.Buffer

		w := lz4.NewWriter(&out)
		fw, err := NewFramingWriter(w)
		require.NoError(t, err)

		var sid pb.SessionIdentification
		sid.ServiceId = pb.NewULID()
		sid.ProtocolId = "blah"

		_, err = fw.WriteMarshal(11, &sid)
		require.NoError(t, err)

		mb := MarshalBytes("hello hzn!")
		_, err = fw.WriteMarshal(30, &mb)
		require.NoError(t, err)

		r := lz4.NewReader(bytes.NewReader(out.Bytes()))

		fr, err := NewFramingReader(r)
		require.NoError(t, err)

		tag, _, err := fr.Next()
		require.NoError(t, err)

		assert.Equal(t, byte(11), tag)

		io.Copy(ioutil.Discard, fr)

		tag, _, err = fr.Next()
		require.NoError(t, err)

		assert.Equal(t, byte(30), tag)
	})
}
