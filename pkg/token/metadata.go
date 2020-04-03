package token

func Metadata(stoken string) (map[string]string, error) {
	md := map[string]string{}

	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, err
	}

	var t Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	for _, h := range t.Unprotected.Headers {
		if h.Key != "" {
			md[h.Key] = h.Sval
		}
	}

	return md, nil
}
