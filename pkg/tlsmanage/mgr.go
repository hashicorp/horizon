package tlsmanage

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/go-acme/lego/v3/certificate"
	"github.com/go-acme/lego/v3/challenge"
	"github.com/go-acme/lego/v3/challenge/dns01"
	"github.com/go-acme/lego/v3/lego"
	"github.com/go-acme/lego/v3/log"
	lego53 "github.com/go-acme/lego/v3/providers/dns/route53"
	"github.com/go-acme/lego/v3/registration"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

type Manager struct {
	cfg ManagerConfig

	lcfg *lego.Config

	email        string
	registration *registration.Resource
	key          crypto.PrivateKey

	hubCert   []byte
	hubIssuer []byte
	hubKey    []byte

	challengeProvider challenge.Provider
	dnsOptions        []dns01.ChallengeOption
}

func (m *Manager) GetEmail() string {
	return m.email
}

func (m *Manager) GetRegistration() *registration.Resource {
	return m.registration
}

func (m *Manager) GetPrivateKey() crypto.PrivateKey {
	return m.key
}

type ManagerConfig struct {
	L           hclog.Logger
	Domain      string
	KeyPath     string
	VaultClient *api.Client
	Staging     bool
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	var (
		m    Manager
		pkey crypto.PrivateKey
		err  error
	)

	if cfg.L == nil {
		cfg.L = hclog.L()
	}

	m.cfg = cfg

	if cfg.KeyPath != "" {
		f, err := os.Open(cfg.KeyPath)
		if err == nil {
			data, err := ioutil.ReadAll(f)
			if err != nil {
				return nil, err
			}

			p, _ := pem.Decode(data)
			pkey = p.Bytes

			cfg.L.Debug("read lego key from path", "path", cfg.KeyPath)
		}
	} else if cfg.VaultClient != nil {
		sec, err := cfg.VaultClient.Logical().Read("/kv/data/lego-key")
		if err != nil {
			return nil, err
		}

		if sec != nil {
			data := sec.Data["data"].(map[string]interface{})

			derkey, err := base64.StdEncoding.DecodeString(data["key"].(string))
			if err != nil {
				return nil, err
			}

			key, err := x509.ParsePKCS8PrivateKey(derkey)
			if err != nil {
				return nil, err
			}

			eckey, ok := key.(*ecdsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("value in vault was not an ecdsa key")
			}

			pkey = eckey
			cfg.L.Debug("read lego key from vault")
		} else {
			ecpkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
			if err != nil {
				return nil, err
			}

			keyBytes, err := x509.MarshalPKCS8PrivateKey(ecpkey)
			if err != nil {
				return nil, err
			}

			_, err = cfg.VaultClient.Logical().Write("/kv/data/lego-key", map[string]interface{}{
				"data": map[string]interface{}{
					"key": keyBytes,
				},
			})
			if err != nil {
				return nil, err
			}

			pkey = ecpkey
			cfg.L.Debug("generated and wrote lego key to vault")
		}
	} else {
		pkey, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			return nil, err
		}

		cfg.L.Debug("using transient lego key")
	}

	m.key = pkey

	m.lcfg = lego.NewConfig(&m)

	if cfg.Staging {
		m.lcfg.CADirURL = lego.LEDirectoryStaging
		cfg.L.Info("configured to use the Let's Encrypt staging service")
	}

	return &m, nil
}

func (m *Manager) SetupRoute53(sess *session.Session, zoneId string) error {
	awsConfig := lego53.NewDefaultConfig()
	awsConfig.HostedZoneID = zoneId
	awsConfig.Client = route53.New(sess)

	prov, err := lego53.NewDNSProviderConfig(awsConfig)
	if err != nil {
		return err
	}

	m.challengeProvider = prov
	return nil
}

func (m *Manager) SetupHubCert(ctx context.Context) error {
	domain := m.cfg.Domain

	log.Logger = hclog.FromContext(ctx).StandardLogger(&hclog.StandardLoggerOptions{InferLevels: true})

	// A client facilitates communication with the CA server.
	client, err := lego.NewClient(m.lcfg)
	if err != nil {
		return err
	}

	client.Challenge.SetDNS01Provider(m.challengeProvider, m.dnsOptions...)
	reg, err := client.Registration.ResolveAccountByKey()
	if err != nil {
		reg, err = client.Registration.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return errors.Wrapf(err, "attempting to register")
		}
	}

	m.registration = reg

	request := certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	}

	cert, err := client.Certificate.Obtain(request)
	if err != nil {
		return errors.Wrapf(err, "attempting to obtain certificate")
	}

	m.hubCert = cert.Certificate
	m.hubIssuer = cert.IssuerCertificate
	m.hubKey = cert.PrivateKey

	return nil
}

func (m *Manager) RefreshFromVault() ([]byte, []byte, error) {
	cert, key, err := m.FetchFromVault()
	if err != nil {
		return nil, nil, err
	}

	m.hubCert = cert
	m.hubKey = key

	return cert, key, nil
}

func (m *Manager) HubMaterial(ctx context.Context) ([]byte, []byte, error) {
	if len(m.hubCert) > 0 {
		return m.hubCert, m.hubKey, nil
	}

	cert, key, err := m.FetchFromVault()
	if err == nil {
		m.hubCert = cert
		m.hubKey = key
		if err != nil {
			return nil, nil, err
		}
		return cert, m.hubKey, nil
	}

	err = m.SetupHubCert(ctx)
	if err != nil {
		return nil, nil, err
	}

	err = m.StoreInVault()
	if err != nil {
		return nil, nil, err
	}

	return m.hubCert, m.hubKey, nil
}

func (m *Manager) Certificate() (tls.Certificate, error) {
	return tls.X509KeyPair(m.hubCert, m.hubKey)
}
