package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"path/filepath"

	"github.com/hashicorp/vault/api"
	"github.com/mitchellh/mapstructure"
)

func SetupVault(vc *api.Client, path string) (ed25519.PublicKey, error) {
	sec, err := vc.Logical().Read(filepath.Join("/transit/keys", path))
	if err != nil {
		return nil, err
	}

	if sec == nil {
		_, err = vc.Logical().Write(filepath.Join("/transit/keys", path), map[string]interface{}{
			"type": "ed25519",
		})

		sec, err = vc.Logical().Read(filepath.Join("/transit/keys", path))
		if err != nil {
			return nil, err
		}

		if sec == nil {
			return nil, fmt.Errorf("vault transit not available")
		}
	}

	type keyData struct {
		PublicKey string `mapstructure:"public_key"`
	}

	var secData struct {
		Keys map[string]keyData `mapstructure:"keys"`
	}

	err = mapstructure.Decode(sec.Data, &secData)
	if err != nil {
		return nil, err
	}

	key, err := base64.StdEncoding.DecodeString(secData.Keys["1"].PublicKey)
	if err != nil {
		return nil, err
	}

	return key, nil
}
