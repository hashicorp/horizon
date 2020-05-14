package tlsmanage

import (
	"encoding/base64"

	"github.com/pkg/errors"
)

var ErrNoTLSMaterial = errors.New("no tls material available")

func (m *Manager) FetchFromVault() ([]byte, []byte, error) {
	sec, err := m.cfg.VaultClient.Logical().Read("/kv/data/hub-tls")
	if err != nil {
		return nil, nil, err
	}

	if sec == nil {
		return nil, nil, ErrNoTLSMaterial
	}

	data, ok := sec.Data["data"].(map[string]interface{})
	if !ok {
		return nil, nil, ErrNoTLSMaterial
	}

	key, err := base64.StdEncoding.DecodeString(data["key"].(string))
	if err != nil {
		return nil, nil, err
	}

	cert, err := base64.StdEncoding.DecodeString(data["certificate"].(string))
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func (m *Manager) StoreInVault() error {
	_, err := m.cfg.VaultClient.Logical().Write("/kv/data/hub-tls", map[string]interface{}{
		"data": map[string]interface{}{
			"key":         m.hubKey,
			"certificate": m.hubCert,
		},
	})

	return err
}
