package tlsmanage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	"github.com/go-acme/lego/v3/certcrypto"
	"github.com/go-acme/lego/v3/challenge/dns01"
	"github.com/go-acme/lego/v3/lego"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDNSProvider struct {
	present struct {
		domain, token, keyAuth string
	}

	cleanup struct {
		domain, token, keyAuth string
	}
}

func (m *mockDNSProvider) Timeout() (timeout time.Duration, interval time.Duration) {
	return time.Second, time.Second
}

func (m *mockDNSProvider) Present(domain string, token string, keyAuth string) error {
	m.present.domain = domain
	m.present.token = token
	m.present.keyAuth = keyAuth
	return nil
}

func (m *mockDNSProvider) CleanUp(domain string, token string, keyAuth string) error {
	m.cleanup.domain = domain
	m.cleanup.token = token
	m.cleanup.keyAuth = keyAuth
	return nil
}

func TestManager(t *testing.T) {
	vc := testutils.SetupVault()

	t.Run("sets up the hub certs", func(t *testing.T) {
		var mdp mockDNSProvider

		mgr, err := NewManager(ManagerConfig{
			Domain: "*.test.cloud",
		})
		require.NoError(t, err)

		mgr.challengeProvider = &mdp

		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		var dnsCheckFqdn string

		mgr.key = priv
		mgr.lcfg = lego.NewConfig(mgr)
		mgr.dnsOptions = append(mgr.dnsOptions,
			dns01.WrapPreCheck(
				func(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
					dnsCheckFqdn = fqdn
					return true, nil
				}),
		)

		mgr.lcfg.CADirURL = "https://127.0.0.1:14000/dir"
		mgr.lcfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
		mgr.lcfg.Certificate.KeyType = certcrypto.EC256

		ctx := context.Background()

		err = mgr.SetupHubCert(ctx)
		require.NoError(t, err)

		tlsCert, err := mgr.Certificate()
		require.NoError(t, err)

		cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
		require.NoError(t, err)

		assert.Equal(t, "*.test.cloud", cert.DNSNames[0])

		assert.Equal(t, "test.cloud", mdp.present.domain)
		assert.Equal(t, "test.cloud", mdp.cleanup.domain)

		assert.Equal(t, "_acme-challenge.test.cloud.", dnsCheckFqdn)
	})

	t.Run("can fetch the hub material from vault", func(t *testing.T) {
		defer vc.Logical().Delete("/kv/metadata/hub-tls")

		var mdp mockDNSProvider

		mgr, err := NewManager(ManagerConfig{
			VaultClient: vc,
		})
		require.NoError(t, err)

		mgr.challengeProvider = &mdp

		key := []byte("this is a bad key")
		cert := []byte("this is a worse cert")

		_, err = vc.Logical().Write("/kv/data/hub-tls", map[string]interface{}{
			"data": map[string]interface{}{
				"key":         key,
				"certificate": cert,
			},
		})
		require.NoError(t, err)

		certOut, keyOut, err := mgr.FetchFromVault()
		require.NoError(t, err)

		assert.Equal(t, key, keyOut)
		assert.Equal(t, cert, certOut)
	})

	t.Run("can store it's material in vault", func(t *testing.T) {
		defer vc.Logical().Delete("/kv/metadata/hub-tls")

		var mdp mockDNSProvider

		mgr, err := NewManager(ManagerConfig{
			VaultClient: vc,
		})
		require.NoError(t, err)

		mgr.challengeProvider = &mdp

		mgr.hubKey = []byte("this is a bad key")
		mgr.hubCert = []byte("this is a worse cert")

		err = mgr.StoreInVault()
		require.NoError(t, err)

		certOut, keyOut, err := mgr.FetchFromVault()
		require.NoError(t, err)

		assert.Equal(t, mgr.hubKey, keyOut)
		assert.Equal(t, mgr.hubCert, certOut)
	})

	t.Run("sets up the hub certs and stores them in vault", func(t *testing.T) {
		defer vc.Logical().Delete("/kv/metadata/hub-tls")

		var mdp mockDNSProvider

		mgr, err := NewManager(ManagerConfig{
			Domain:      "*.test.cloud",
			VaultClient: vc,
		})
		require.NoError(t, err)

		mgr.challengeProvider = &mdp

		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		var dnsCheckFqdn string

		mgr.key = priv
		mgr.lcfg = lego.NewConfig(mgr)
		mgr.dnsOptions = append(mgr.dnsOptions,
			dns01.WrapPreCheck(
				func(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
					dnsCheckFqdn = fqdn
					return true, nil
				}),
		)

		mgr.lcfg.CADirURL = "https://127.0.0.1:14000/dir"
		mgr.lcfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
		mgr.lcfg.Certificate.KeyType = certcrypto.EC256

		ctx := context.Background()

		bcert, bkey, err := mgr.HubMaterial(ctx)
		require.NoError(t, err)

		tlsCert, err := tls.X509KeyPair(bcert, bkey)
		require.NoError(t, err)

		cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
		require.NoError(t, err)

		assert.Equal(t, "*.test.cloud", cert.DNSNames[0])

		assert.Equal(t, "test.cloud", mdp.present.domain)
		assert.Equal(t, "test.cloud", mdp.cleanup.domain)

		assert.Equal(t, "_acme-challenge.test.cloud.", dnsCheckFqdn)

		vcert, vkey, err := mgr.FetchFromVault()
		require.NoError(t, err)

		assert.Equal(t, bcert, vcert)
		assert.Equal(t, bkey, vkey)

		mgr.hubCert = nil
		mgr.lcfg = nil

		bcert2, bkey2, err := mgr.HubMaterial(ctx)
		require.NoError(t, err)

		assert.Equal(t, bcert, bcert2)
		assert.Equal(t, bkey, bkey2)
	})

}
