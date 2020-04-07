package agent

import "strings"

type Label struct {
	Name, Value string
}

func (l *Label) String() string {
	return l.Name + "=" + l.Value
}

func ParseLabel(s string) Label {
	idx := strings.IndexByte(s, '=')
	if idx == -1 {
		return Label{Name: s}
	}

	return Label{Name: s[:idx], Value: s[idx+1:]}
}
