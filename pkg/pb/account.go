package pb

import (
	"bytes"
	"errors"
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
