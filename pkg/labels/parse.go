// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package labels

import "strings"

func ParseLabel(s string) (string, string) {
	idx := strings.IndexByte(s, '=')
	if idx == -1 {
		return s, ""
	}

	return s[:idx], s[idx+1:]
}
