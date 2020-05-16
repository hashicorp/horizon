package token

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/vault/api"
	"golang.org/x/crypto/blake2b"
)

type TokenCreator struct {
	Role            pb.TokenRole
	Issuer          string
	AccountId       *pb.ULID
	AccuntNamespace string
	Capabilities    map[pb.Capability]string
	Metadata        map[string]string
	ValidDuration   time.Duration

	RawCapabilities []pb.TokenCapability
}

const (
	Magic = 0x47
)

func (c *TokenCreator) body() ([]byte, error) {
	capa := c.RawCapabilities

	for k, v := range c.Capabilities {
		capa = append(capa, pb.TokenCapability{
			Capability: k,
			Value:      v,
		})
	}

	body := &pb.Token_Body{
		Role: c.Role,
		Id:   pb.NewULID(),
		Account: &pb.Account{
			Namespace: c.AccuntNamespace,
			AccountId: c.AccountId,
		},
		Capabilities: capa,
	}

	if c.ValidDuration > 0 {
		body.ValidUntil = pb.NewTimestamp(time.Now().Add(c.ValidDuration))
	}

	return body.Marshal()
}

func (c *TokenCreator) EncodeHMAC(key []byte, keyId string) (string, error) {
	var t pb.Token

	t.Metadata = &pb.Headers{}

	for k, v := range c.Metadata {
		t.Metadata.Headers = append(t.Metadata.Headers, &pb.KVPair{
			Key:   k,
			Value: v,
		})
	}

	data, err := c.body()
	if err != nil {
		return "", err
	}

	t.Body = data

	h, err := blake2b.New256(key)
	if err != nil {
		return "", err
	}

	h.Write(data)

	t.Signatures = []*pb.Signature{
		{
			SigType:   pb.BLAKE2HMAC,
			KeyId:     keyId,
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

func (c *TokenCreator) EncodeED25519(key ed25519.PrivateKey, keyId string) (string, error) {
	var t pb.Token

	t.Metadata = &pb.Headers{}

	for k, v := range c.Metadata {
		t.Metadata.Headers = append(t.Metadata.Headers, &pb.KVPair{
			Key:   k,
			Value: v,
		})
	}

	data, err := c.body()
	if err != nil {
		return "", err
	}

	t.Body = data

	t.Signatures = []*pb.Signature{
		{
			SigType:   pb.ED25519,
			KeyId:     keyId,
			Signature: ed25519.Sign(key, data),
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

func (c *TokenCreator) EncodeED25519WithVault(vc *api.Client, path, keyId string) (string, error) {
	var t pb.Token

	t.Metadata = &pb.Headers{}

	for k, v := range c.Metadata {
		t.Metadata.Headers = append(t.Metadata.Headers, &pb.KVPair{
			Key:   k,
			Value: v,
		})
	}

	data, err := c.body()
	if err != nil {
		return "", err
	}

	secret, err := vc.Logical().Write(filepath.Join("/transit/sign", path), map[string]interface{}{
		"input":                base64.StdEncoding.EncodeToString(data),
		"marshaling_algorithm": "jws",
	})

	if err != nil {
		return "", err
	}

	ct, ok := secret.Data["signature"].(string)
	if !ok {
		return "", fmt.Errorf("vault response missing ciphertext")
	}

	sig, err := base64.RawURLEncoding.DecodeString(ct[9:])
	if err != nil {
		return "", err
	}

	t.Body = data

	t.Signatures = []*pb.Signature{
		{
			SigType:   pb.ED25519,
			KeyId:     keyId,
			Signature: sig,
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
