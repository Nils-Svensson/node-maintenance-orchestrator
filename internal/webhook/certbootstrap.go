package webhook

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
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

// Secret data field names and PEM block type labels.
const (
	caCertField     = "ca.crt"
	caKeyField      = "ca.key"
	pemCertificate  = "CERTIFICATE"
	pemECPrivateKey = "EC PRIVATE KEY"
)

// CertBootstrapper manages self-signed TLS credentials for the webhook and
// metrics servers.
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
//
// Webhook and metrics cert generation are each optional: set the corresponding
// fields to enable them. Both share the same CA Secret.
type CertBootstrapper struct {
	Client     client.Client
	Namespace  string
	SecretName string

	// Webhook cert — only generated when CertDir and ServiceName are set.
	CertDir           string
	ServiceName       string
	WebhookConfigName string

	// Metrics cert — only generated when MetricsCertDir and MetricsServiceName
	// are set. Includes localhost/127.0.0.1 SANs for port-forward debugging.
	MetricsCertDir     string
	MetricsServiceName string
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
	logger := log.FromContext(ctx).WithName("cert-bootstrapper")

	caCert, caKey, caRotated, err := b.ensureCA(ctx)
	if err != nil {
		return err
	}

	if b.CertDir != "" && (caRotated || serverCertNeedsRenewal(b.CertDir)) {
		logger.V(1).Info("generating webhook server certificate", "service", b.ServiceName, "namespace", b.Namespace)
		serverCert, serverKey, err := generateServerCert(caCert, caKey, b.ServiceName, b.Namespace, nil, nil)
		if err != nil {
			return fmt.Errorf("generating webhook server cert: %w", err)
		}
		if err := writeCertsToDisk(b.CertDir, serverCert, serverKey); err != nil {
			return fmt.Errorf("writing webhook certs to disk: %w", err)
		}
		if err := b.patchCABundle(ctx, caCert); err != nil {
			return err
		}
		logger.Info("webhook certificates ready")
	}

	if b.MetricsCertDir != "" && (caRotated || serverCertNeedsRenewal(b.MetricsCertDir)) {
		logger.V(1).Info("generating metrics server certificate", "service", b.MetricsServiceName, "namespace", b.Namespace)
		metricsCert, metricsKey, err := generateServerCert(caCert, caKey, b.MetricsServiceName, b.Namespace,
			[]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return fmt.Errorf("generating metrics server cert: %w", err)
		}
		if err := writeCertsToDisk(b.MetricsCertDir, metricsCert, metricsKey); err != nil {
			return fmt.Errorf("writing metrics certs to disk: %w", err)
		}
		logger.Info("metrics certificates ready")
	}

	return nil
}

// ensureCA returns the shared CA cert and key, creating or rotating the Secret
// as needed. It handles concurrent Create calls from multiple pods.
// rotated is true whenever a new CA was generated (create, rotation, or create
// race loss), signalling EnsureCerts to regenerate all server certs.
func (b *CertBootstrapper) ensureCA(ctx context.Context) (caCert, caKey []byte, rotated bool, err error) {
	logger := log.FromContext(ctx).WithName("cert-bootstrapper")

	secret := &corev1.Secret{}
	getErr := b.Client.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.SecretName}, secret)

	switch {
	case getErr == nil && !caIsExpiring(secret):
		logger.V(1).Info("reusing existing CA", "secret", b.SecretName)
		return secret.Data[caCertField], secret.Data[caKeyField], false, nil

	case getErr == nil && caIsExpiring(secret):
		logger.Info("CA is expiring, rotating", "secret", b.SecretName)
		caCert, caKey, err = generateCA()
		if err != nil {
			return nil, nil, false, fmt.Errorf("rotating CA: %w", err)
		}
		patch := client.MergeFrom(secret.DeepCopy())
		secret.Data[caCertField] = caCert
		secret.Data[caKeyField] = caKey
		if err := b.Client.Patch(ctx, secret, patch); err != nil {
			return nil, nil, false, fmt.Errorf("patching CA secret: %w", err)
		}
		logger.Info("CA rotated", "secret", b.SecretName)
		return caCert, caKey, true, nil

	case apierrors.IsNotFound(getErr):
		logger.Info("creating CA secret", "secret", b.SecretName, "namespace", b.Namespace)
		caCert, caKey, err = generateCA()
		if err != nil {
			return nil, nil, false, fmt.Errorf("generating CA: %w", err)
		}
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: b.SecretName, Namespace: b.Namespace},
			Data:       map[string][]byte{caCertField: caCert, caKeyField: caKey},
		}
		if createErr := b.Client.Create(ctx, newSecret); createErr != nil {
			if !apierrors.IsAlreadyExists(createErr) {
				return nil, nil, false, fmt.Errorf("creating CA secret: %w", createErr)
			}
			// Another pod won the Create race — read the winner's Secret.
			logger.V(1).Info("CA secret already exists (create race), reading winner's secret")
			if err := b.Client.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.SecretName}, secret); err != nil {
				return nil, nil, false, fmt.Errorf("reading CA secret after create race: %w", err)
			}
			// Server certs on this pod's disk don't exist yet — treat as rotated.
			return secret.Data[caCertField], secret.Data[caKeyField], true, nil
		}
		logger.Info("CA secret created", "secret", b.SecretName)
		return caCert, caKey, true, nil

	default:
		return nil, nil, false, fmt.Errorf("getting CA secret: %w", getErr)
	}
}

func caIsExpiring(secret *corev1.Secret) bool {
	block, _ := pem.Decode(secret.Data[caCertField])
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return time.Until(cert.NotAfter) < renewBefore
}

// serverCertNeedsRenewal returns true if the on-disk server cert is missing,
// unreadable, or expiring within renewBefore. It does NOT check CA parentage —
// the caRotated flag in EnsureCerts handles the CA-change case.
func serverCertNeedsRenewal(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if err != nil {
		return true
	}
	block, _ := pem.Decode(data)
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

	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generating serial number: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "node-maintenance-orchestrator-ca"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(cryptorand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	caCert = pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: certDER})
	caKey = pem.EncodeToMemory(&pem.Block{Type: pemECPrivateKey, Bytes: keyDER})
	return caCert, caKey, nil
}

func generateServerCert(caCertPEM, caKeyPEM []byte, serviceName, namespace string, extraDNS []string, extraIPs []net.IP) (serverCert, serverKey []byte, err error) {
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

	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generating serial number: %w", err)
	}

	dnsNames := []string{
		serviceName,
		fmt.Sprintf("%s.%s", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
	}
	dnsNames = append(dnsNames, extraDNS...)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[2]},
		DNSNames:     dnsNames,
		IPAddresses:  extraIPs,
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(cryptorand.Reader, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating server cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling server key: %w", err)
	}

	serverCert = pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: certDER})
	serverKey = pem.EncodeToMemory(&pem.Block{Type: pemECPrivateKey, Bytes: keyDER})
	return serverCert, serverKey, nil
}

func writeCertsToDisk(dir string, serverCert, serverKey []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating cert dir: %w", err)
	}
	files := map[string][]byte{"tls.crt": serverCert, "tls.key": serverKey}
	for name, content := range files {
		if err := atomicWriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// atomicWriteFile writes data to a temp file in the same directory, then renames
// it over path. os.Rename is atomic on Linux, so the certwatcher never reads a
// partially written cert.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cert-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after successful rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing data: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	return os.Rename(tmpPath, path)
}

func (b *CertBootstrapper) patchCABundle(ctx context.Context, caBundle []byte) error {
	logger := log.FromContext(ctx).WithName("cert-bootstrapper")
	logger.Info("patching caBundle", "webhookConfig", b.WebhookConfigName)

	// The VWC may not exist yet if pods start before kubectl apply reaches the
	// webhook resources (../manager precedes ../webhook in kustomization.yaml).
	// Retry until it appears or the context deadline is exceeded.
	webhookConfig := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	for {
		err := b.Client.Get(ctx, types.NamespacedName{Name: b.WebhookConfigName}, webhookConfig)
		if err == nil {
			break
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting webhook config %q: %w", b.WebhookConfigName, err)
		}
		logger.V(1).Info("ValidatingWebhookConfiguration not found yet, retrying", "webhookConfig", b.WebhookConfigName)
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for webhook config %q: %w", b.WebhookConfigName, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	logger.V(1).Info("found ValidatingWebhookConfiguration", "webhooks", len(webhookConfig.Webhooks))

	patch := client.MergeFrom(webhookConfig.DeepCopy())
	for i := range webhookConfig.Webhooks {
		webhookConfig.Webhooks[i].ClientConfig.CABundle = caBundle
	}
	if err := b.Client.Patch(ctx, webhookConfig, patch); err != nil {
		return fmt.Errorf("patching caBundle on %q: %w", b.WebhookConfigName, err)
	}
	logger.Info("caBundle patched", "webhookConfig", b.WebhookConfigName, "bytes", len(caBundle))
	return nil
}
