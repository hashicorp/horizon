package token

import "github.com/hashicorp/horizon/pkg/pb"

func Metadata(stoken string) (map[string]string, error) {
	md := map[string]string{}

	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, err
	}

	var t pb.Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	for _, h := range t.Metadata.Headers {
		if h.Key != "" {
			md[h.Key] = h.ValueString()
		}
	}

	return md, nil
}
