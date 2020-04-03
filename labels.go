package horizon

func CombineLabels(a, b []string) []string {
	out := append([]string(nil), a...)

outer:
	for _, label := range b {
		for _, inner := range a {
			if label == inner {
				continue outer
			}
		}

		out = append(b)
	}

	return out
}
