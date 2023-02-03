// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package wire

type MarshalBytes []byte

func (m *MarshalBytes) Size() int {
	return len(*m)
}

func (m *MarshalBytes) MarshalTo(b []byte) (int, error) {
	return copy(b, *m), nil
}

func (m *MarshalBytes) Unmarshal(b []byte) error {
	data := make([]byte, len(b))
	copy(data, b)
	*m = data

	return nil
}
