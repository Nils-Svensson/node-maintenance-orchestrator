package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// GenerateExpiredCA returns a PEM-encoded CA cert and key whose NotAfter is
// daysUntilExpiry days from now. Pass a value < 30 to place it inside the
// operator's renewal window, triggering rotation on the next startup.
func GenerateExpiredCA(daysUntilExpiry int32) (caCert, caKey []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	validity := time.Duration(daysUntilExpiry) * 24 * time.Hour
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nmo-test-ca"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(nil, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	caCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	caKey = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return caCert, caKey, nil
}
