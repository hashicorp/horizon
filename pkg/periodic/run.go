// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package periodic

import (
	"context"
	"time"
)

func Run(ctx context.Context, period time.Duration, f func()) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f()
		}
	}
}
