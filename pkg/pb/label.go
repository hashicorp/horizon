package pb

import (
	"sort"
	strings "strings"
)

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

	pls := &ls
	sort.Sort(pls)

	return pls
}

func (ls *LabelSet) SpecString() string {
	var out []string

	for _, label := range ls.Labels {
		if label.Value == "" {
			out = append(out, strings.ToLower(label.Name))
		} else {
			out = append(out, strings.ToLower(label.Name+"="+label.Value))
		}
	}

	sort.Strings(out)

	return strings.Join(out, ",")
}

func (ls *LabelSet) Len() int {
	return len(ls.Labels)
}

func (ls *LabelSet) Less(i, j int) bool {
	a := ls.Labels[i]
	b := ls.Labels[j]

	if a.Name < b.Name {
		return true
	}

	if a.Value < b.Value {
		return true
	}

	return false
}

func (ls *LabelSet) Swap(i, j int) {
	ls.Labels[i], ls.Labels[j] = ls.Labels[j], ls.Labels[i]
}
