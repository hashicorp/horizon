// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package labels

import (
	"sort"
	"strings"
)

func CompressLabels(v []string) string {
	sort.Strings(v)

	return strings.ToLower(strings.Join(v, ", "))
}
