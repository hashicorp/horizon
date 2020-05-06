package pb

import (
	"bytes"
	"errors"

	"github.com/mr-tron/base58"
	"golang.org/x/crypto/blake2b"
)

// Collapse the Account to an unambigious sequence
func (a *Account) Key() []byte {
	var buf bytes.Buffer
	buf.WriteString(a.Namespace)
	buf.WriteRune('!')
	buf.Write(a.AccountId.Bytes())

	return buf.Bytes()
}

func (a *Account) StringKey() string {
	var buf bytes.Buffer
	buf.WriteString(a.Namespace)
	buf.WriteRune('!')
	buf.WriteString(a.AccountId.String())

	return buf.String()
}

func (a *Account) SpecString() string {
	return a.StringKey()
}

func (a *Account) String() string {
	return a.StringKey()
}

// Hash the data and present a stable hash key useful for use
// in things like file and url paths.
func (a *Account) HashKey() string {
	h, _ := blake2b.New256(nil)
	h.Write([]byte("hznaccount"))
	h.Write([]byte(a.Namespace))
	h.Write(a.AccountId.Bytes())

	return base58.Encode(h.Sum(nil))
}

var ErrInvalidAccount = errors.New("invalid account key")

func AccountFromKey(k []byte) (*Account, error) {
	pos := bytes.IndexRune(k, '!')
	if pos == -1 {
		return nil, ErrInvalidAccount
	}

	namespace := k[:pos]
	id := k[pos+1:]

	return &Account{
		Namespace: string(namespace),
		AccountId: ULIDFromBytes(id),
	}, nil
}

func AccountFromStringKey(k []byte) (*Account, error) {
	pos := bytes.IndexRune(k, '!')
	if pos == -1 {
		return nil, ErrInvalidAccount
	}

	namespace := k[:pos]
	id := k[pos+1:]

	return &Account{
		Namespace: string(namespace),
		AccountId: ULIDFromBytes(id),
	}, nil
}
