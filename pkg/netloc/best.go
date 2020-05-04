package netloc

import (
	"sort"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
)

type BestInput struct {
	Count  int
	Local  []*pb.NetworkLocation
	Remote []*pb.NetworkLocation

	Latency func(addr string) error
}

// Given 2 sets of network locations, return the best N (count) ones that local should use
// to connect to remote
func FindBest(input *BestInput) ([]*pb.NetworkLocation, error) {
	var best []*pb.NetworkLocation

	best = append(best, input.Remote...)

	cards := make([]int, len(best))

	for i, rloc := range best {
		card := 0

		for _, lloc := range input.Local {
			c := lloc.Cardinality(rloc)
			if c > card {
				card = c
			}
		}

		cards[i] = card
	}

	sort.Slice(best, func(i, j int) bool {
		// j and i are flipped here so the results are sorted desc rather than asc
		return cards[j] < cards[i]
	})

	if len(best) > input.Count {
		best = best[input.Count:]
	}

	if input.Latency != nil {
		latency := make([]time.Duration, len(best))
		available := make([]bool, len(best))

		type result struct {
			latency time.Duration
			ok      bool
			pos     int
		}

		results := make(chan result)

		for i, loc := range best {
			go func(i int, loc *pb.NetworkLocation) {
				t := time.Now()
				addr := loc.Addresses[0]
				err := input.Latency(addr)
				latency := time.Since(t)

				var ok bool

				if err == nil {
					ok = true
				}

				results <- result{
					latency: latency,
					ok:      ok,
					pos:     i,
				}
			}(i, loc)
		}

		for range latency {
			res := <-results
			latency[res.pos] = res.latency
			available[res.pos] = res.ok
		}

		sort.Slice(best, func(i, j int) bool {
			// j and i are flipped here so the results are sorted desc rather than asc
			return latency[i] < latency[j]
		})

		var prune []*pb.NetworkLocation

		for i, loc := range best {
			if available[i] {
				prune = append(prune, loc)
			}
		}

		best = prune
	}

	return best, nil
}
