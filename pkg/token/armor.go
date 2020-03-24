package token

import (
	"github.com/mr-tron/base58"
)

func Armor(token []byte) string {
	return base58.Encode(token)
}

func RemoveArmor(token string) ([]byte, error) {
	return base58.Decode(token)
}
