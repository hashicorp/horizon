package tlsmanager

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
	t.Run("sets up the hub certs", func(t *testing.T) {
		var mdp mockDNSProvider

		var mgr Manager

		mgr.challengeProvider = &mdp

		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		var dnsCheckFqdn string

		mgr.key = priv
		mgr.cfg = lego.NewConfig(&mgr)
		mgr.dnsOptions = append(mgr.dnsOptions,
			dns01.WrapPreCheck(
				func(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
					dnsCheckFqdn = fqdn
					return true, nil
				}),
		)

		mgr.cfg.CADirURL = "https://127.0.0.1:14000/dir"
		mgr.cfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
		mgr.cfg.Certificate.KeyType = certcrypto.EC256

		ctx := context.Background()

		err = mgr.SetupHubCert(ctx, "*.test.cloud")
		require.NoError(t, err)

		cert, err := x509.ParseCertificate(mgr.hubCert)
		require.NoError(t, err)

		assert.Equal(t, "*.test.cloud", cert.DNSNames[0])

		assert.Equal(t, "test.cloud", mdp.present.domain)
		assert.Equal(t, "test.cloud", mdp.cleanup.domain)

		assert.Equal(t, "_acme-challenge.test.cloud.", dnsCheckFqdn)
	})
}
