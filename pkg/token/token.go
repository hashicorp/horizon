package token

func (h *Headers) AccountId() []byte {
	for _, hdr := range h.Headers {
		if hdr.Ikey == LabelAccountId {
			return hdr.Bval
		}
	}

	return nil
}
