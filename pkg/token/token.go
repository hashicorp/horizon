package token

import (
	strings "strings"

	"github.com/hashicorp/horizon/pkg/pb"
)

func (t *ValidToken) Account() *pb.Account {
	return t.Body.Account
}

func (t *ValidToken) HasCapability(target pb.Capability) (bool, string) {
	for _, capa := range t.Body.Capabilities {
		if capa.Capability == target {
			return true, capa.Value
		}
	}

	return false, ""
}

func (t *ValidToken) AllowAccount(ns string) bool {
	// First, this token has to have the capability to access other accounts
	ok, val := t.HasCapability(pb.ACCESS)
	if !ok {
		return false
	}

	// Then if the access namespace is the same as the requestd namespace, allow
	// it.
	if ns == val {
		return true
	}

	// If the access namespace is not a valid prefix of requested one, then def
	if !strings.HasPrefix(ns, val) {
		return false
	}

	// Verify that after the prefix is a separater so that the access namespace
	// doesn't accidentally match a partial namespace
	if ns[len(val)] != '/' {
		return false
	}

	return true
}
