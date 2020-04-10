package labels

import "strings"

func ParseLabel(s string) (string, string) {
	idx := strings.IndexByte(s, '=')
	if idx == -1 {
		return s, ""
	}

	return s[:idx], s[idx+1:]
}
