package pb

import (
	"database/sql/driver"
	fmt "fmt"
	"sort"
	strings "strings"

	"github.com/lib/pq"
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

// Call if manually creating a LabelSet once it's filled
func (ls *LabelSet) Finalize() {
	sort.Sort(ls)
}

func (ls *LabelSet) Value() (driver.Value, error) {
	return ls.AsStringArray(), nil
}

func (ls *LabelSet) AsStringArray() pq.StringArray {
	var out pq.StringArray

	for _, e := range ls.Labels {
		out = append(out, e.Name+"="+e.Value)
	}

	return out
}

func (ls *LabelSet) Scan(src interface{}) error {
	ary, ok := src.(pq.StringArray)
	if !ok {
		return fmt.Errorf("only accepts a string array")
	}

	for _, pair := range ary {
		var name, val string
		eqIdx := strings.IndexByte(pair, '=')
		if eqIdx != -1 {
			name = pair[:eqIdx]
			val = pair[eqIdx+1:]
		} else {
			name = pair
		}

		ls.Labels = append(ls.Labels, &Label{
			Name:  name,
			Value: val,
		})
	}

	return nil
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

func (ls *LabelSet) Matches(o *LabelSet) bool {
	// utilize the property that LabelSets must be sorted!

	idx := 0
	max := len(o.Labels)

	for _, lbl := range ls.Labels {
		for {
			if idx >= max {
				return false
			}

			ssLabel := o.Labels[idx]
			idx++
			if ssLabel.Name == lbl.Name && ssLabel.Value == lbl.Value {
				break
			}

		}
	}

	return true
}
