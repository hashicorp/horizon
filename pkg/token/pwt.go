package token

import (
	"bytes"
	"crypto/ed25519"

	"golang.org/x/crypto/blake2b"
)

type TokenCreator struct {
	Issuer    string
	AccountId []byte
}

const (
	Magic = 0x47
)

func (c *TokenCreator) protected() ([]byte, error) {
	headers := &Headers{
		Headers: []*Header{
			{
				Ikey: LabelAccountId,
				Bval: c.AccountId,
			},
		},
	}

	return headers.Marshal()
}

func (c *TokenCreator) EncodeHMAC(key []byte) (string, error) {
	var t Token

	t.Unprotected = &Headers{
		Headers: []*Header{
			{
				Ikey: LabelAlgo,
				Ival: ValueHMAC,
			},
		},
	}

	data, err := c.protected()
	if err != nil {
		return "", err
	}

	t.Protected = data

	h, err := blake2b.New256(key)
	if err != nil {
		return "", err
	}

	h.Write(data)

	t.Signatures = []*Signature{
		{
			Signature: h.Sum(nil),
		},
	}

	tdata, err := t.Marshal()
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer

	buf.WriteByte(Magic)
	buf.Write(tdata)

	return Armor(buf.Bytes()), nil
}

func (c *TokenCreator) EncodeED25519(key ed25519.PrivateKey) ([]byte, error) {
	var t Token

	t.Unprotected = &Headers{
		Headers: []*Header{
			{
				Ikey: LabelAlgo,
				Ival: ValueED25519,
			},
		},
	}

	data, err := c.protected()
	if err != nil {
		return nil, err
	}

	t.Protected = data

	t.Signatures = []*Signature{
		{
			Signature: ed25519.Sign(key, data),
		},
	}

	tdata, err := t.Marshal()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	buf.WriteByte(Magic)
	buf.Write(tdata)

	return buf.Bytes(), nil
}
