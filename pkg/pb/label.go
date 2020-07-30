package pb

import (
	"database/sql/driver"
	fmt "fmt"
	"sort"
	strings "strings"
	"unicode"
	"unicode/utf8"

	"github.com/lib/pq"
)

func ParseLabelSet(s string) *LabelSet {
	var ls LabelSet

	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		idx := strings.IndexByte(part, '=')
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
	pls.Finalize()

	return pls
}

func MakeLabels(args ...string) *LabelSet {
	if len(args)%2 != 0 {
		panic("args must be a even")
	}

	var out LabelSet

	for i := 0; i < len(args); i += 2 {
		out.Labels = append(out.Labels, &Label{
			Name:  strings.ToLower(args[i]),
			Value: strings.ToLower(args[i+1]),
		})
	}

	return &out
}

func (ls *LabelSet) Add(name, value string) *LabelSet {
	dup := &LabelSet{}

	dup.Labels = append(dup.Labels, ls.Labels...)

	dup.Labels = append(dup.Labels, &Label{
		Name:  name,
		Value: value,
	})

	dup.Finalize()

	return dup
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

func (ls *LabelSet) String() string {
	return strings.Join(ls.AsStringArray(), ", ")
}

func (ls *LabelSet) Contains(name, value string) bool {
	for _, lbl := range ls.Labels {
		if strings.EqualFold(lbl.Name, name) && strings.EqualFold(lbl.Value, value) {
			return true
		}
	}

	return false
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

func lowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		r += ('a' - 'A')
	}

	return r
}

// lessFold reports whether s and t, interpreted as UTF-8 strings,
// have s less than t under Unicode case-folding, which is a more general
// form of case-insensitivity.
func lessFold(s, t string) bool {
	for s != "" && t != "" {
		// Extract first rune from each string.
		var sr, tr rune
		if s[0] < utf8.RuneSelf {
			sr, s = rune(s[0]), s[1:]
		} else {
			r, size := utf8.DecodeRuneInString(s)
			sr, s = r, s[size:]
		}
		if t[0] < utf8.RuneSelf {
			tr, t = rune(t[0]), t[1:]
		} else {
			r, size := utf8.DecodeRuneInString(t)
			tr, t = r, t[size:]
		}

		// If they match, keep going; if not, fold and check relationship

		// Easy case.
		if sr == tr {
			continue
		}

		sr = unicode.SimpleFold(sr)
		tr = unicode.SimpleFold(tr)

		if sr == tr {
			continue
		}

		return sr < tr
	}

	// One string is empty. Are both?
	return s < t
}

func (ls *LabelSet) Less(i, j int) bool {
	a := ls.Labels[i]
	b := ls.Labels[j]

	if lessFold(a.Name, b.Name) {
		return true
	}

	if strings.EqualFold(a.Name, b.Name) && lessFold(a.Value, b.Value) {
		return true
	}

	return false
}

func (ls *LabelSet) Swap(i, j int) {
	a := ls.Labels[i]
	b := ls.Labels[j]

	ls.Labels[i] = b
	ls.Labels[j] = a
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
			if strings.EqualFold(ssLabel.Name, lbl.Name) && strings.EqualFold(ssLabel.Value, lbl.Value) {
				break
			}

		}
	}

	return true
}

func (ls *LabelSet) Combine(o *LabelSet) *LabelSet {
	var out *LabelSet

	out.Labels = append([]*Label{}, ls.Labels...)

outer:
	for _, outer := range o.Labels {
		for _, inner := range ls.Labels {
			if strings.EqualFold(outer.Name, inner.Name) && strings.EqualFold(outer.Value, inner.Value) {
				continue outer
			}
		}

		out.Labels = append(out.Labels, outer)
	}

	out.Finalize()

	return out
}
