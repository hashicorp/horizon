package tlsmanager

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
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
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/pkg/errors"
)

type Manager struct {
	cfg *lego.Config

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

func (m *Manager) Init(keyPath string) error {
	var pkey ed25519.PrivateKey

	f, err := os.Open(keyPath)
	if err == nil {
		data, err := ioutil.ReadAll(f)
		if err != nil {
			return err
		}

		p, _ := pem.Decode(data)
		pkey = p.Bytes
	} else {
		_, pkey, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
	}

	m.key = pkey

	m.cfg = lego.NewConfig(m)

	return nil
}

func (m *Manager) SetupRoute53(sess *session.Session, zoneId string) error {
	var awsConfig lego53.Config
	awsConfig.HostedZoneID = zoneId
	awsConfig.Client = route53.New(sess)

	prov, err := lego53.NewDNSProviderConfig(&awsConfig)
	if err != nil {
		return err
	}

	m.challengeProvider = prov
	return nil
}

func (m *Manager) SetupHubCert(ctx context.Context, domain string) error {
	log.Logger = ctxlog.L(ctx).StandardLogger(&hclog.StandardLoggerOptions{InferLevels: true})

	// A client facilitates communication with the CA server.
	client, err := lego.NewClient(m.cfg)
	if err != nil {
		return err
	}

	client.Challenge.SetDNS01Provider(m.challengeProvider, m.dnsOptions...)
	reg, err := client.Registration.Register(registration.RegisterOptions{
		TermsOfServiceAgreed: true,
	})
	if err != nil {
		return errors.Wrapf(err, "attempting to register")
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

	b, _ := pem.Decode(cert.Certificate)
	m.hubCert = b.Bytes

	b, _ = pem.Decode(cert.IssuerCertificate)
	m.hubIssuer = b.Bytes

	b, _ = pem.Decode(cert.PrivateKey)
	m.hubKey = cert.PrivateKey

	return nil
}
