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

	t.Run("considers a priority label when picking between equal private addresses", func(t *testing.T) {
		var h Hub

		h.location = mkLocs("192.168.2.1", "10.0.1.1")
		h.location[0].Labels = pb.ParseLabelSet("type=private,dc=test,priority=1")
		h.location[1].Labels = pb.ParseLabelSet("type=private,dc=test2,priority=10")

		tgt := mkLocs("192.168.2.2", "10.0.1.2")
		tgt[0].Labels = h.location[0].Labels
		tgt[1].Labels = h.location[1].Labels

		addr, err := h.pickAddress(tgt)
		require.NoError(t, err)

		assert.Equal(t, "10.0.1.2", addr)
	})

	t.Run("picks a public address of the hub doesn't know it's location", func(t *testing.T) {
		var h Hub

		target := []*pb.NetworkLocation{
			{
				Addresses: []string{"10.74.6.232", "172.17.0.2"},
				Labels:    pb.ParseLabelSet("instance-id=i-02af81bda71fbfd46, subnet-id=subnet-00d964b25bff9c50b, type=private, vpc-id=vpc-09b89aed62e06c836"),
			},
			{
				Addresses: []string{"54.149.212.61", "54.149.212.61"},
				Labels:    pb.ParseLabelSet("asn=AS16509, country=US, type=public, vpc-id=vpc-09b89aed62e06c836"),
			},
		}
		target[1].Labels.Labels = append(target[1].Labels.Labels, &pb.Label{
			Name:  "location",
			Value: "45.8491, -119.7143",
		})

		addr, err := h.pickAddress(target)
		require.NoError(t, err)

		assert.Equal(t, "54.149.212.61", addr)
	})

	t.Run("works in the real words", func(t *testing.T) {
		var h Hub
		h.location = []*pb.NetworkLocation{
			{
				Labels:    pb.ParseLabelSet("instance-id=i-03082ea61de06c620, subnet-id=subnet-00d964b25bff9c50b, type=private, vpc-id=vpc-09b89aed62e06c836"),
				Addresses: []string{"10.74.6.134", "172.17.0.2"},
			},
			{
				Labels:    pb.ParseLabelSet("asn=AS16509, country=US, type=public, vpc-id=vpc-09b89aed62e06c836"),
				Addresses: []string{"18.237.45.161", "18.237.45.161"},
			},
		}

		h.location[1].Labels.Labels = append(h.location[1].Labels.Labels, &pb.Label{
			Name:  "location",
			Value: "45.8491, -119.7143",
		})

		target := []*pb.NetworkLocation{
			{
				Addresses: []string{"10.74.6.232", "172.17.0.2"},
				Labels:    pb.ParseLabelSet("instance-id=i-02af81bda71fbfd46, subnet-id=subnet-00d964b25bff9c50b, type=private, vpc-id=vpc-09b89aed62e06c836"),
			},
			{
				Addresses: []string{"54.149.212.61", "54.149.212.61"},
				Labels:    pb.ParseLabelSet("asn=AS16509, country=US, type=public, vpc-id=vpc-09b89aed62e06c836"),
			},
		}
		target[1].Labels.Labels = append(target[1].Labels.Labels, &pb.Label{
			Name:  "location",
			Value: "45.8491, -119.7143",
		})

		addr, err := h.pickAddress(target)
		require.NoError(t, err)

		assert.Equal(t, "54.149.212.61", addr)
	})

}
