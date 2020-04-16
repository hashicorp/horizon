package token

import (
	"crypto/ed25519"
	"crypto/subtle"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
)

var (
	ErrBadToken      = errors.New("bad token")
	ErrNoLongerValid = errors.New("token no longer valid")
)

// Exposed as a function to be changed by the tests to mimic clock issues
var timeNow = time.Now

type ValidToken struct {
	Body  *pb.Token_Body
	Token *pb.Token
	Raw   []byte
	KeyId string
}

func checkTokenValidity(b *pb.Token_Body) error {
	now := timeNow()

	if now.Before(b.Id.Time()) {
		return ErrNoLongerValid
	}

	if b.ValidUntil == nil {
		return nil
	}

	if now.After(b.ValidUntil.Time()) {
		return ErrNoLongerValid
	}

	return nil
}

func CheckTokenHMAC(stoken string, key []byte) (*ValidToken, error) {
	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, ErrBadToken
	}

	var t pb.Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	h, err := blake2b.New256(key)
	if err != nil {
		return nil, err
	}

	h.Write(t.Body)

	computed := h.Sum(nil)

	var (
		keyId string
		ok    bool
	)

	for _, sig := range t.Signatures {
		if sig.SigType != pb.BLAKE2HMAC {
			continue
		}

		if subtle.ConstantTimeCompare(computed, sig.Signature) == 1 {
			ok = true
			keyId = sig.KeyId
			break
		}
	}

	if !ok {
		return nil, errors.Wrapf(ErrBadToken, "no signatures matched (%d)", len(t.Signatures))
	}

	var body pb.Token_Body

	err = body.Unmarshal(t.Body)
	if err != nil {
		return nil, errors.Wrapf(err, "corruption in protected headers")
	}

	err = checkTokenValidity(&body)
	if err != nil {
		return nil, err
	}

	vt := &ValidToken{
		Body:  &body,
		Token: &t,
		Raw:   token,
		KeyId: keyId,
	}

	return vt, nil
}

func CheckTokenED25519(stoken string, key ed25519.PublicKey) (*ValidToken, error) {
	token, err := RemoveArmor(stoken)
	if err != nil {
		return nil, err
	}

	if token[0] != Magic {
		return nil, ErrBadToken
	}

	var t pb.Token

	err = t.Unmarshal(token[1:])
	if err != nil {
		return nil, err
	}

	var (
		keyId string
		ok    bool
	)

	for _, sig := range t.Signatures {
		if sig.SigType != pb.ED25519 {
			continue
		}

		if ed25519.Verify(key, t.Body, sig.Signature) {
			keyId = sig.KeyId
			ok = true
			break
		}
	}

	if !ok {
		return nil, errors.Wrapf(ErrBadToken, "no signatures matched")
	}

	var body pb.Token_Body

	err = body.Unmarshal(t.Body)
	if err != nil {
		return nil, errors.Wrapf(err, "corruption in protected headers")
	}

	err = checkTokenValidity(&body)
	if err != nil {
		return nil, err
	}

	vt := &ValidToken{
		Body:  &body,
		Token: &t,
		Raw:   token,
		KeyId: keyId,
	}

	return vt, nil
}
