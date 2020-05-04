package netloc

import (
	"testing"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindBest(t *testing.T) {
	t.Run("find locations that match exactly", func(t *testing.T) {
		lloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"1.1.1.1"},
				Labels:    pb.ParseLabelSet("dc=x"),
			},
		}

		rloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"2.2.2.2"},
				Labels:    pb.ParseLabelSet("dc=x"),
			},
			{
				Addresses: []string{"3.3.3.3"},
				Labels:    pb.ParseLabelSet("dc=y"),
			},
		}

		best, err := FindBest(&BestInput{
			Count:  10,
			Local:  lloc,
			Remote: rloc,
		})
		require.NoError(t, err)

		require.Equal(t, 2, len(best))

		assert.Equal(t, "2.2.2.2", best[0].Addresses[0])
	})

	t.Run("find locations that with the highest cardinality", func(t *testing.T) {
		lloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"1.1.1.1"},
				Labels:    pb.ParseLabelSet("dc=x,az=y"),
			},
		}

		rloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"2.2.2.2"},
				Labels:    pb.ParseLabelSet("dc=x,az=q"),
			},
			{
				Addresses: []string{"3.3.3.3"},
				Labels:    pb.ParseLabelSet("dc=y"),
			},
		}

		best, err := FindBest(&BestInput{
			Count:  10,
			Local:  lloc,
			Remote: rloc,
		})
		require.NoError(t, err)

		require.Equal(t, 2, len(best))

		assert.Equal(t, "2.2.2.2", best[0].Addresses[0])
	})

	t.Run("can use latencys of the hosts to sort them", func(t *testing.T) {
		lloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"1.1.1.1"},
				Labels:    pb.ParseLabelSet("dc=x,az=y"),
			},
		}

		rloc := []*pb.NetworkLocation{
			{
				Addresses: []string{"2.2.2.2"},
				Labels:    pb.ParseLabelSet("dc=x,az=q"),
			},
			{
				Addresses: []string{"3.3.3.3"},
				Labels:    pb.ParseLabelSet("dc=y"),
			},
		}

		best, err := FindBest(&BestInput{
			Count:  10,
			Local:  lloc,
			Remote: rloc,
			Latency: func(addr string) error {
				if addr == "2.2.2.2" {
					time.Sleep(time.Second)
				}

				return nil
			},
		})
		require.NoError(t, err)

		require.Equal(t, 2, len(best))

		assert.Equal(t, "3.3.3.3", best[0].Addresses[0])
	})
}
