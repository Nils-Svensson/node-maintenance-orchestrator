/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	maintenancev1alpha1 "github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/controller"
	wh "github.com/Nils-Svensson/node-maintenance-orchestrator/internal/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(maintenancev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	var webhookEnabled bool
	var operatorNamespace string
	var webhookServiceName string
	var webhookConfigName string
	var tlsCertSecretName string
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.BoolVar(&webhookEnabled, "webhook-enabled", false,
		"Enable the validating webhook server. Requires the ValidatingWebhookConfiguration to be pre-installed.")
	flag.StringVar(&operatorNamespace, "operator-namespace", "nmo-system",
		"Namespace in which the operator runs. Used to store the webhook TLS secret.")
	flag.StringVar(&webhookServiceName, "webhook-service-name", "node-maintenance-orchestrator-webhook-service",
		"Service name that fronts the webhook server. Used as the TLS certificate SAN.")
	flag.StringVar(&webhookConfigName, "webhook-config-name",
		"node-maintenance-orchestrator-validating-webhook-configuration",
		"Name of the ValidatingWebhookConfiguration whose caBundle is managed by the operator.")
	flag.StringVar(&tlsCertSecretName, "tls-cert-secret-name", "node-maintenance-orchestrator-tls-cert",
		"Name of the Secret used to store the shared TLS CA cert and key (used by both webhook and metrics cert bootstrapper).")
	var metricsServiceName string
	flag.StringVar(&metricsServiceName, "metrics-service-name", "node-maintenance-orchestrator-ctrl-manager-metrics-service",
		"Service name that fronts the metrics server. Used as the TLS certificate SAN.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	restConfig := ctrl.GetConfigOrDie()

	// Bootstrap self-signed TLS certs when no external cert paths are provided.
	// The bootstrapper shares a single CA Secret across all server certs and
	// patches the ValidatingWebhookConfiguration caBundle for the webhook.
	const defaultWebhookCertDir = "/tmp/k8s-webhook-server/serving-certs"
	const defaultMetricsCertDir = "/tmp/k8s-metrics-server/serving-certs"
	var bootstrapper *wh.CertBootstrapper
	needsBootstrap := (webhookEnabled && len(webhookCertPath) == 0) || (secureMetrics && len(metricsCertPath) == 0)
	if needsBootstrap {
		bootstrapClient, err := client.New(restConfig, client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "unable to create bootstrap client")
			os.Exit(1)
		}
		b := &wh.CertBootstrapper{
			Client:     bootstrapClient,
			Namespace:  operatorNamespace,
			SecretName: tlsCertSecretName,
		}
		if webhookEnabled && len(webhookCertPath) == 0 {
			b.CertDir = defaultWebhookCertDir
			b.ServiceName = webhookServiceName
			b.WebhookConfigName = webhookConfigName
		}
		if secureMetrics && len(metricsCertPath) == 0 {
			b.MetricsCertDir = defaultMetricsCertDir
			b.MetricsServiceName = metricsServiceName
		}
		setupLog.Info("bootstrapping certificates",
			"namespace", operatorNamespace,
			"secretName", tlsCertSecretName,
			"webhook", webhookEnabled,
			"secureMetrics", secureMetrics)
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := b.EnsureCerts(bootstrapCtx); err != nil {
			// Non-fatal: bootstrap fails when running outside the cluster (e.g. make run).
			setupLog.Error(err, "cert bootstrap failed; running without managed certificates")
		} else {
			if b.CertDir != "" {
				webhookCertPath = defaultWebhookCertDir
			}
			if b.MetricsCertDir != "" {
				metricsCertPath = defaultMetricsCertDir
				setupLog.Info("metrics certificates bootstrapped", "certDir", defaultMetricsCertDir)
			}
			if b.CertDir != "" {
				setupLog.Info("webhook certificates bootstrapped", "certDir", defaultWebhookCertDir)
			}
			bootstrapper = b
		}
	}

	// webhookReady is true only when the webhook is enabled AND valid certs exist
	// (either from a successful bootstrap or from a user-provided cert path).
	// When false, the webhook server is not created and the manager runs without it.
	webhookReady := webhookEnabled && len(webhookCertPath) > 0

	var webhookServer webhook.Server
	if webhookReady {
		setupLog.Info("initializing webhook server",
			"certDir", webhookCertPath, "certName", webhookCertName, "keyName", webhookCertKey)
		webhookServer = webhook.NewServer(webhook.Options{
			TLSOpts:  tlsOpts,
			CertDir:  webhookCertPath,
			CertName: webhookCertName,
			KeyName:  webhookCertKey,
		})
	}

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		// Hardcoded to be stable across versions
		LeaderElectionID: "ab079f3e.nmoo.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if bootstrapper != nil {
		if err := mgr.Add(bootstrapper); err != nil {
			setupLog.Error(err, "unable to register cert renewer")
			os.Exit(1)
		}
	}

	if err := (&controller.NodeMaintenancePlanReconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("node-maintenance-orchestrator"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeMaintenancePlan")
		os.Exit(1)
	}
	if webhookReady {
		validator := &wh.NodeMaintenancePlanValidator{Client: mgr.GetClient()}
		// Register via mgr.GetWebhookServer() — this triggers the sync.Once that adds the
		// server to the manager's runnable group. Calling Register directly on the local
		// webhookServer variable bypasses that Once and the server never starts.
		mgr.GetWebhookServer().Register(
			"/validate-maintenance-nmoo-io-v1alpha1-nodemaintenanceplan",
			&webhook.Admission{Handler: validator},
		)
		setupLog.Info("validating webhook registered")
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	if webhookReady {
		if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
			setupLog.Error(err, "unable to set up webhook ready check")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
