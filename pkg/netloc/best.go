package netloc

import (
	"context"
	"sort"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
)

type BestInput struct {
	Count      int
	Local      []*pb.NetworkLocation
	Remote     []*pb.NetworkLocation
	PublicOnly bool

	Latency func(addr string) error
}

// Given 2 sets of network locations, return the best N (count) ones that local should use
// to connect to remote
func FindBestLive(ctx context.Context, input *BestInput, locs chan *pb.NetworkLocation) error {
	defer close(locs)

	var best []*pb.NetworkLocation

	if input.PublicOnly {
		for _, loc := range input.Remote {
			if !loc.Labels.Contains("type", "private") {
				best = append(best, loc)
			}
		}
	} else {
		best = append(best, input.Remote...)
	}

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

	if input.Latency == nil {
		if input.Count != 0 && input.Count > len(best) {
			best = best[:input.Count]
		}

		for _, loc := range best {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case locs <- loc:
				// ok
			}
		}

		return nil
	}

	if input.Latency != nil {
		type result struct {
			ok  bool
			pos int
		}

		results := make(chan result)

		for i, loc := range best {
			go func(i int, loc *pb.NetworkLocation) {
				addr := loc.Addresses[0]
				err := input.Latency(addr)

				var ok bool

				if err == nil {
					ok = true
				}

				results <- result{
					ok:  ok,
					pos: i,
				}
			}(i, loc)
		}

		for i := 0; i < len(best); i++ {
			res := <-results
			if input.Count == 0 || i < input.Count {
				if res.ok {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case locs <- best[res.pos]:
						// ok
					}
				}
			}
		}
	}

	return nil
}

// Given 2 sets of network locations, return the best N (count) ones that local should use
// to connect to remote
func FindBest(input *BestInput) ([]*pb.NetworkLocation, error) {
	var best []*pb.NetworkLocation

	if input.PublicOnly {
		for _, loc := range input.Remote {
			if !loc.Labels.Contains("type", "private") {
				best = append(best, loc)
			}
		}
	} else {
		best = append(best, input.Remote...)
	}

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

				hclog.L().Info("latency", "addr", addr, "latency", latency)

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

		var prune []*pb.NetworkLocation

		for i, loc := range best {
			if available[i] {
				prune = append(prune, loc)
			}
		}

		best = prune

		sort.Slice(best, func(i, j int) bool {
			return latency[i] < latency[j]
		})
	}

	if len(best) > input.Count {
		best = best[:input.Count]
	}

	return best, nil
}
