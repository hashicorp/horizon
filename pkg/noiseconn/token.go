package noiseconn

import (
	"encoding/base64"
	"encoding/binary"

	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
)

var ErrBadToken = errors.New("bad token")

func DecodeToken(token string) (*Token, []byte, []byte, error) {
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, nil, nil, err
	}

	if len(data) < 2 {
		return nil, nil, nil, ErrBadToken
	}

	sz := binary.BigEndian.Uint16(data)

	if len(data) < int(sz+2) {
		return nil, nil, nil, ErrBadToken
	}

	body := data[2 : sz+2]

	sig := data[sz+2:]

	var t Token

	err = t.Unmarshal(body)

	if err != nil {
		return nil, nil, nil, err
	}

	return &t, body, sig, nil
}

func (t *Token) TunnelID() string {
	u, err := uuid.FromBytes(t.Id)
	if err != nil {
		panic(err)
	}

	return u.String()
}
