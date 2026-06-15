package webhook

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// pemCACertWithExpiry generates a minimal self-signed CA cert expiring at notAfter.
func pemCACertWithExpiry(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(99),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestGenerateCA(t *testing.T) {
	caCert, caKey, err := generateCA()
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	block, _ := pem.Decode(caCert)
	if block == nil {
		t.Fatal("CA cert PEM is nil")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	if !cert.IsCA {
		t.Error("want IsCA=true")
	}
	if !cert.BasicConstraintsValid {
		t.Error("want BasicConstraintsValid=true")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("want KeyUsageCertSign")
	}
	if cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("want KeyUsageCRLSign")
	}
	if time.Until(cert.NotAfter) < 89*24*time.Hour {
		t.Errorf("NotAfter too soon: %v", cert.NotAfter)
	}
	if keyBlock, _ := pem.Decode(caKey); keyBlock == nil {
		t.Fatal("CA key PEM is nil")
	}
}

func TestGenerateServerCert(t *testing.T) {
	caCert, caKey, err := generateCA()
	if err != nil {
		t.Fatal(err)
	}

	serverCert, serverKey, err := generateServerCert(caCert, caKey, "webhook-svc", "nmo-system")
	if err != nil {
		t.Fatalf("generateServerCert: %v", err)
	}

	block, _ := pem.Decode(serverCert)
	if block == nil {
		t.Fatal("server cert PEM is nil")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}

	// All four DNS SAN forms must be present.
	wantSANs := []string{
		"webhook-svc",
		"webhook-svc.nmo-system",
		"webhook-svc.nmo-system.svc",
		"webhook-svc.nmo-system.svc.cluster.local",
	}
	sanSet := make(map[string]bool, len(cert.DNSNames))
	for _, n := range cert.DNSNames {
		sanSet[n] = true
	}
	for _, want := range wantSANs {
		if !sanSet[want] {
			t.Errorf("SAN %q missing; DNSNames=%v", want, cert.DNSNames)
		}
	}

	// Must carry the server-auth EKU so the API server accepts it.
	hasServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
		}
	}
	if !hasServerAuth {
		t.Error("want ExtKeyUsageServerAuth")
	}

	// Server cert must not be a CA.
	if cert.IsCA {
		t.Error("server cert must not be IsCA")
	}

	// Cert must chain to the CA.
	caBlock, _ := pem.Decode(caCert)
	caParsed, _ := x509.ParseCertificate(caBlock.Bytes)
	pool := x509.NewCertPool()
	pool.AddCert(caParsed)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     "webhook-svc.nmo-system.svc",
		CurrentTime: time.Now(),
	}); err != nil {
		t.Errorf("cert does not chain to CA: %v", err)
	}

	if keyBlock, _ := pem.Decode(serverKey); keyBlock == nil {
		t.Fatal("server key PEM is nil")
	}

	// Invalid CA cert PEM must return an error.
	if _, _, err := generateServerCert([]byte("bad"), caKey, "svc", "ns"); err == nil {
		t.Error("want error for invalid CA cert PEM")
	}
	// Invalid CA key PEM must return an error.
	if _, _, err := generateServerCert(caCert, []byte("bad"), "svc", "ns"); err == nil {
		t.Error("want error for invalid CA key PEM")
	}
}

func TestCAIsExpiring(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name     string
		data     map[string][]byte
		wantTrue bool
	}{
		{
			name:     "nil data",
			wantTrue: true,
		},
		{
			name:     "invalid PEM",
			data:     map[string][]byte{"ca.crt": []byte("not-pem")},
			wantTrue: true,
		},
		{
			name:     "28 days left — inside renewal window",
			data:     map[string][]byte{"ca.crt": pemCACertWithExpiry(t, now.Add(28*24*time.Hour))},
			wantTrue: true,
		},
		{
			name:     "60 days left — outside renewal window",
			data:     map[string][]byte{"ca.crt": pemCACertWithExpiry(t, now.Add(60*24*time.Hour))},
			wantTrue: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Secret{Data: tc.data}
			if got := caIsExpiring(s); got != tc.wantTrue {
				t.Errorf("caIsExpiring=%v, want %v", got, tc.wantTrue)
			}
		})
	}
}
