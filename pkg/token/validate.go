package token

import (
	"crypto/ed25519"
	"crypto/subtle"

	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
)

var ErrBadToken = errors.New("bad token")

func CheckTokenHMAC(stoken string, key []byte) (*Headers, error) {
	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, ErrBadToken
	}

	var t Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	var algo int64

	for _, hdr := range t.Unprotected.Headers {
		if hdr.Ikey == LabelAlgo {
			algo = hdr.Ival
		}
	}

	if algo != ValueHMAC {
		return nil, errors.Wrapf(ErrBadToken, "incorrect algo")
	}

	h, err := blake2b.New256(key)
	if err != nil {
		return nil, err
	}

	h.Write(t.Protected)

	computed := h.Sum(nil)

	var ok bool

	for _, sig := range t.Signatures {
		if subtle.ConstantTimeCompare(computed, sig.Signature) == 1 {
			ok = true
			break
		}
	}

	if !ok {
		return nil, errors.Wrapf(ErrBadToken, "no signatures matched (%d)", len(t.Signatures))
	}

	var prot Headers

	err = prot.Unmarshal(t.Protected)
	if err != nil {
		return nil, errors.Wrapf(err, "corruption in protected headers")
	}

	return &prot, nil
}

func CheckTokenED25519(stoken string, key ed25519.PublicKey) (*Headers, error) {
	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, ErrBadToken
	}

	var t Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	var algo int64

	for _, hdr := range t.Unprotected.Headers {
		if hdr.Ikey == LabelAlgo {
			algo = hdr.Ival
		}
	}

	if algo != ValueED25519 {
		return nil, errors.Wrapf(ErrBadToken, "incorrect algo")
	}

	var ok bool

	for _, sig := range t.Signatures {
		if ed25519.Verify(key, t.Protected, sig.Signature) {
			ok = true
			break
		}
	}

	if !ok {
		return nil, errors.Wrapf(ErrBadToken, "no signatures matched")
	}

	var prot Headers

	err = prot.Unmarshal(t.Protected)
	if err != nil {
		return nil, errors.Wrapf(err, "corruption in protected headers")
	}

	return &prot, nil
}
