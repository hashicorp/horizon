package horizon

import "github.com/hashicorp/horizon/pkg/agent"

func CombineLabels(a, b []agent.Label) []agent.Label {
	out := append([]agent.Label(nil), a...)

outer:
	for _, outer := range b {
		for _, inner := range a {
			if outer.Name == inner.Name && outer.Value == inner.Value {
				continue outer
			}
		}

		out = append(out, outer)
	}

	return out
}
