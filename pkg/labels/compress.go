package labels

import (
	"sort"
	"strings"
)

func CompressLabels(v []string) string {
	sort.Strings(v)

	return strings.ToLower(strings.Join(v, ", "))
}
