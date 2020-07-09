package utils

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

func SelfSignedCert() ([]byte, []byte, error) {
	tlspub, tlspriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now()

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Acme Co"},
		},
		NotBefore: time.Now(),
		NotAfter:  notBefore.Add(5 * time.Minute),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"hub.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, tlspub, tlspriv)

	var certBuf, keyBuf bytes.Buffer

	pem.Encode(&certBuf, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	keybytes, err := x509.MarshalPKCS8PrivateKey(tlspriv)
	if err != nil {
		return nil, nil, err
	}

	pem.Encode(&keyBuf, &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keybytes,
	})

	return certBuf.Bytes(), keyBuf.Bytes(), nil
}

func TrustedTLSConfig(cert []byte) (*tls.Config, error) {
	parsedHubCert, err := x509.ParseCertificate(cert)
	if err != nil {
		return nil, err
	}

	var tlscfg tls.Config

	tlscfg.RootCAs = x509.NewCertPool()
	tlscfg.RootCAs.AddCert(parsedHubCert)

	return &tlscfg, nil
}
