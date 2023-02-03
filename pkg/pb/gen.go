// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

//go:generate sh -c "protoc --go-json_out=. --gogoslick_out=plugins=grpc:. *.proto"
package pb
