// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package wire

import (
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
)

func Now() *pb.Timestamp {
	t := time.Now()

	return &pb.Timestamp{
		Sec:  uint64(t.Unix()),
		Nsec: uint64(t.Nanosecond()),
	}
}
