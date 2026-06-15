package webhook

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	renewBefore          = 30 * 24 * time.Hour
	certValidity         = 90 * 24 * time.Hour
	renewalCheckInterval = 12 * time.Hour
)

// CertBootstrapper manages self-signed TLS credentials for the webhook server.
//
// Design for HA (multiple replicas):
//   - The CA cert and key are stored in a shared Secret so every pod signs its
//     server certificate with the same CA.
//   - Each pod generates its own server cert from the shared CA at startup; no
//     server cert is persisted to the Secret.
//   - The ValidatingWebhookConfiguration caBundle holds the single shared CA
//     cert, so the API server trusts every pod's server cert.
//   - Create races are handled with an AlreadyExists retry: whichever pod wins
//     the race to create the Secret sets the CA; the loser reads and uses it.
type CertBootstrapper struct {
	Client            client.Client
	Namespace         string
	CertDir           string
	SecretName        string
	WebhookConfigName string
	ServiceName       string
}

// Start implements manager.Runnable. It runs a background loop that calls
// EnsureCerts every renewalCheckInterval so certs rotate before expiry without
// requiring a pod restart. The manager calls this after its cache is synced.
func (b *CertBootstrapper) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("cert-renewer")
	ticker := time.NewTicker(renewalCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := b.EnsureCerts(ctx); err != nil {
				logger.Error(err, "cert renewal failed")
			} else {
				logger.V(1).Info("cert renewal check complete")
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// EnsureCerts is idempotent and safe to call on every pod startup.
func (b *CertBootstrapper) EnsureCerts(ctx context.Context) error {
	caCert, caKey, err := b.ensureCA(ctx)
	if err != nil {
		return err
	}

	serverCert, serverKey, err := generateServerCert(caCert, caKey, b.ServiceName, b.Namespace)
	if err != nil {
		return fmt.Errorf("generating server cert: %w", err)
	}

	if err := b.writeToDisk(serverCert, serverKey); err != nil {
		return fmt.Errorf("writing certs to disk: %w", err)
	}

	return b.patchCABundle(ctx, caCert)
}

// ensureCA returns the shared CA cert and key, creating or rotating the Secret
// as needed. It handles concurrent Create calls from multiple pods.
func (b *CertBootstrapper) ensureCA(ctx context.Context) (caCert, caKey []byte, err error) {
	secret := &corev1.Secret{}
	getErr := b.Client.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.SecretName}, secret)

	switch {
	case getErr == nil && !caIsExpiring(secret):
		// Happy path: existing CA is still valid.
		return secret.Data["ca.crt"], secret.Data["ca.key"], nil

	case getErr == nil && caIsExpiring(secret):
		// CA is within the renewal window — rotate it.
		caCert, caKey, err = generateCA()
		if err != nil {
			return nil, nil, fmt.Errorf("rotating CA: %w", err)
		}
		patch := client.MergeFrom(secret.DeepCopy())
		secret.Data["ca.crt"] = caCert
		secret.Data["ca.key"] = caKey
		if err := b.Client.Patch(ctx, secret, patch); err != nil {
			return nil, nil, fmt.Errorf("patching CA secret: %w", err)
		}
		return caCert, caKey, nil

	case apierrors.IsNotFound(getErr):
		// Secret doesn't exist yet — generate and create it.
		caCert, caKey, err = generateCA()
		if err != nil {
			return nil, nil, fmt.Errorf("generating CA: %w", err)
		}
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: b.SecretName, Namespace: b.Namespace},
			Data:       map[string][]byte{"ca.crt": caCert, "ca.key": caKey},
		}
		if createErr := b.Client.Create(ctx, newSecret); createErr != nil {
			if !apierrors.IsAlreadyExists(createErr) {
				return nil, nil, fmt.Errorf("creating CA secret: %w", createErr)
			}
			// Another pod won the Create race — read the winner's Secret.
			if err := b.Client.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.SecretName}, secret); err != nil {
				return nil, nil, fmt.Errorf("reading CA secret after create race: %w", err)
			}
			return secret.Data["ca.crt"], secret.Data["ca.key"], nil
		}
		return caCert, caKey, nil

	default:
		return nil, nil, fmt.Errorf("getting CA secret: %w", getErr)
	}
}

func caIsExpiring(secret *corev1.Secret) bool {
	block, _ := pem.Decode(secret.Data["ca.crt"])
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return time.Until(cert.NotAfter) < renewBefore
}

func generateCA() (caCert, caKey []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "node-maintenance-orchestrator-ca"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(certValidity),
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

func generateServerCert(caCertPEM, caKeyPEM []byte, serviceName, namespace string) (serverCert, serverKey []byte, err error) {
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		return nil, nil, fmt.Errorf("decoding CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA cert: %w", err)
	}

	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return nil, nil, fmt.Errorf("decoding CA key PEM")
	}
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA key: %w", err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("generating server key: %w", err)
	}

	dnsNames := []string{
		serviceName,
		fmt.Sprintf("%s.%s", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsNames[2]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(nil, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating server cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling server key: %w", err)
	}

	serverCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	serverKey = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return serverCert, serverKey, nil
}

func (b *CertBootstrapper) writeToDisk(serverCert, serverKey []byte) error {
	if err := os.MkdirAll(b.CertDir, 0700); err != nil {
		return fmt.Errorf("creating cert dir: %w", err)
	}
	files := map[string][]byte{"tls.crt": serverCert, "tls.key": serverKey}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(b.CertDir, name), content, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

func (b *CertBootstrapper) patchCABundle(ctx context.Context, caBundle []byte) error {
	webhookConfig := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := b.Client.Get(ctx, types.NamespacedName{Name: b.WebhookConfigName}, webhookConfig); err != nil {
		return fmt.Errorf("getting webhook config %q: %w", b.WebhookConfigName, err)
	}
	patch := client.MergeFrom(webhookConfig.DeepCopy())
	for i := range webhookConfig.Webhooks {
		webhookConfig.Webhooks[i].ClientConfig.CABundle = caBundle
	}
	return b.Client.Patch(ctx, webhookConfig, patch)
}
