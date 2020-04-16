package pb

import strings "strings"

func ParseLabelSet(s string) *LabelSet {
	var ls LabelSet

	for _, part := range strings.Split(s, ",") {
		idx := strings.IndexByte(strings.TrimSpace(part), '=')
		if idx == -1 {
			ls.Labels = append(ls.Labels, &Label{
				Name: part,
			})
		} else {
			ls.Labels = append(ls.Labels, &Label{
				Name:  part[:idx],
				Value: part[idx+1:],
			})
		}
	}

	return &ls
}
