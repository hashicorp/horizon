package token

import "github.com/oklog/ulid"

func (h *Headers) AccountId() ulid.ULID {
	for _, hdr := range h.Headers {
		if hdr.Ikey == LabelAccountId {
			var id ulid.ULID
			copy(id[:], hdr.Bval)
			return id
		}
	}

	return ulid.ULID{}
}
