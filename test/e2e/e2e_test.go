//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/test/utils"
)

const (
	namespace              = "nmo-system"
	serviceAccountName     = "node-maintenance-orchestrator-controller-manager"
	metricsServiceName     = "node-maintenance-orchestrator-ctrl-manager-metrics-service"
	metricsRoleBindingName = "node-maintenance-orchestrator-metrics-binding"
	webhookConfigName      = "node-maintenance-orchestrator-validating-webhook-configuration"
	webhookSecretName      = "node-maintenance-orchestrator-tls-cert"
	projectImage           = "example.com/node-maintenance-orchestrator:v0.0.1"
)

var (
	controllerPodName string
	workerNodes       []string
)

var _ = Describe("Manager", Ordered, func() {
	BeforeAll(func() {
		By("building the manager image")
		cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
		output, err := utils.Run(cmd)
		_, _ = fmt.Fprintf(GinkgoWriter, "docker-build output:\n%s\n", output)
		Expect(err).NotTo(HaveOccurred(), "Failed to build the manager image")

		By("loading the manager image into Kind")
		err = utils.LoadImageToKindClusterWithName(projectImage)
		Expect(err).NotTo(HaveOccurred(), "Failed to load image into Kind")

		By("creating manager namespace")
		cmd = exec.Command("kubectl", "create", "ns", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		// Install the ServiceMonitor CRD so the API server accepts the ServiceMonitor
		// resource included in config/prometheus. The full Prometheus Operator is not
		// needed; only the CRD registration is required for `make deploy` to succeed.
		By("installing Prometheus Operator ServiceMonitor CRD")
		cmd = exec.Command("kubectl", "apply", "--server-side",
			"-f", "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install ServiceMonitor CRD")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("listing worker nodes")
		cmd = exec.Command("kubectl", "get", "nodes",
			"--selector=!node-role.kubernetes.io/control-plane",
			"-o", "jsonpath={.items[*].metadata.name}")
		nodeOutput, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "failed to list worker nodes")
		workerNodes = strings.Fields(strings.TrimSpace(nodeOutput))
		Expect(len(workerNodes)).To(BeNumerically(">=", 5),
			"e2e tests require at least 5 worker nodes")

		By("waiting for controller pods to be running and ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get",
				"pods", "-l", "control-plane=controller-manager",
				"-o", "go-template={{ range .items }}"+
					"{{ if not .metadata.deletionTimestamp }}"+
					"{{ .metadata.name }}"+
					"{{ \"\\n\" }}{{ end }}{{ end }}",
				"-n", namespace,
			)
			podOutput, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			podNames := utils.GetNonEmptyLines(podOutput)
			g.Expect(podNames).To(HaveLen(2), "expected 2 controller pods running")
			controllerPodName = podNames[0]
			for _, podName := range podNames {
				cmd = exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "pod %s should be Ready", podName)
			}
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the webhook caBundle to be populated")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
				webhookConfigName,
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty(), "caBundle should be set by the operator")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", "-l", "control-plane=controller-manager",
				"-n", namespace, "--all-containers=true", "--tail=100")
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(2), "expected 2 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=node-maintenance-orchestrator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName,
					"--ignore-not-found=true")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics",
					"-n", namespace, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			})

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {"drop": ["ALL"]},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {"type": "RuntimeDefault"}
							}
						}],
						"serviceAccountName": "%s",
						"nodeSelector": {"node-role.kubernetes.io/control-plane": ""},
						"tolerations": [{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}]
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should reject requests to the metrics endpoint without authorization", func() {
			By("creating a curl pod with no bearer token")
			cmd := exec.Command("kubectl", "run", "curl-metrics-unauth", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -s -o /dev/null -w '%%{http_code}' -k https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {"drop": ["ALL"]},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {"type": "RuntimeDefault"}
							}
						}],
						"serviceAccountName": "%s",
						"nodeSelector": {"node-role.kubernetes.io/control-plane": ""},
						"tolerations": [{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}]
					}
				}`, metricsServiceName, namespace, serviceAccountName))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics-unauth pod")

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics-unauth",
					"-n", namespace, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			})

			By("waiting for the pod to complete")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", "curl-metrics-unauth",
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the response was 401 Unauthorized")
			cmd = exec.Command("kubectl", "logs", "curl-metrics-unauth", "-n", namespace)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("401"),
				"expected 401 from unauthenticated request, got: %s", output)
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("NodeMaintenancePlan", Ordered, func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)

		AfterEach(func() {
			By("deleting all NMPs")
			cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should adopt and cordon nodes, then release on plan deletion", func() {
			target := workerNodes[0]
			nmpName := "e2e-cordon"

			By("creating NMP with cordon enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e cordon test"
  cordon:
    enabled: true
`, nmpName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for node to be cordoned")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"))
			}).Should(Succeed())

			By("verifying managed-by annotation is set")
			cmd = exec.Command("kubectl", "get", "node", target,
				"-o", "jsonpath={.metadata.annotations.maintenance\\.nmoo\\.io/managed-by}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(nmpName))

			By("deleting the NMP and waiting for cleanup")
			cmd = exec.Command("kubectl", "delete", "nmp", nmpName, "--wait=true", "--timeout=60s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying node is uncordoned")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("false"), BeEmpty()))
			}).Should(Succeed())

			By("verifying managed-by annotation is removed")
			cmd = exec.Command("kubectl", "get", "node", target,
				"-o", "jsonpath={.metadata.annotations.maintenance\\.nmoo\\.io/managed-by}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(BeEmpty())
		})

		It("should drain pods when cordon and drain are enabled", func() {
			target := workerNodes[1]
			nmpName := "e2e-drain"
			deployName := "e2e-drain-workload"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a test workload on the target node")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 1
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for test pod to be running on the target node")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", "default",
					"-l", fmt.Sprintf("app=%s", deployName),
					"--field-selector", fmt.Sprintf("spec.nodeName=%s,status.phase=Running", target),
					"-o", "name")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
			}).Should(Succeed())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e drain test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainSucceeded condition")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", `jsonpath={.status.conditions[?(@.type=="DrainSucceeded")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}).Should(Succeed())

			By("verifying no pods from the workload remain on the target node")
			cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
				"-l", fmt.Sprintf("app=%s", deployName),
				"--field-selector", fmt.Sprintf("spec.nodeName=%s", target),
				"-o", "name")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty())
		})

		It("should report DrainBlocked when a PodDisruptionBudget prevents eviction, then complete after PDB is removed", func() {
			target := workerNodes[2]
			nmpName := "e2e-pdb"
			deployName := "e2e-pdb-workload"
			pdbName := "e2e-pdb-block"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "pdb", pdbName, "-n", "default", "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload on the target node")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 1
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating a PDB that blocks all evictions")
			pdbYAML := fmt.Sprintf(`
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: %s
  namespace: default
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: %s
`, pdbName, deployName)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(pdbYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e PDB blocking test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainBlocked condition")
			Eventually(nmpCondition(nmpName, "DrainBlocked")).Should(Succeed())

			By("deleting the PDB to unblock drain")
			cmd = exec.Command("kubectl", "delete", "pdb", pdbName, "-n", "default")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for drain to complete after PDB removal")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying no non-terminating pods remain on the target node")
			cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
				"-l", fmt.Sprintf("app=%s", deployName),
				"--field-selector", fmt.Sprintf("spec.nodeName=%s", target),
				"-o", "name")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty())
		})

		It("should block drain for an uncontrolled pod, then drain successfully after enabling force", func() {
			target := workerNodes[3]
			nmpName := "e2e-force"
			podName := "e2e-uncontrolled"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "pod", podName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("creating a pod with no owning controller on the target node")
			podYAML := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
spec:
  nodeName: %s
  terminationGracePeriodSeconds: 1
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.9
`, podName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(podYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for uncontrolled pod to be Running")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName, "-n", "default",
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"))
			}).Should(Succeed())

			By("creating NMP with drain enabled and force disabled (default)")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e force drain test"
  cordon:
    enabled: true
  drain:
    enabled: true
    options:
      force: false
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainBlocked due to uncontrolled pod")
			Eventually(nmpCondition(nmpName, "DrainBlocked")).Should(Succeed())

			By("patching NMP to enable force, which allows eviction of uncontrolled pods")
			cmd = exec.Command("kubectl", "patch", "nmp", nmpName,
				"--type=merge", "-p", `{"spec":{"drain":{"options":{"force":true}}}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for drain to succeed after enabling force")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the uncontrolled pod has been deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName, "-n", "default",
					"--ignore-not-found=true", "-o", "name")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(BeEmpty())
			}).Should(Succeed())
		})

		It("should clean up nodes when plan is deleted mid-drain", func() {
			target := workerNodes[0]
			nmpName := "e2e-delete-mid-drain"
			deployName := "e2e-delete-mid-drain-workload"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload with a slow preStop hook")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 120
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        lifecycle:
          preStop:
            sleep:
              seconds: 90
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e delete mid-drain test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainInProgress — eviction issued, pod is terminating")
			Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

			By("deleting the NMP while drain is in progress")
			cmd = exec.Command("kubectl", "delete", "nmp", nmpName, "--wait=true", "--timeout=60s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "NMP should be deleted with finalizer completing successfully")

			By("verifying the node is uncordoned after plan deletion")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("false"), BeEmpty()))
			}).Should(Succeed())

			By("verifying managed-by annotation is removed after plan deletion")
			cmd = exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(BeEmpty())
		})

		It("should detect drift and release node when manually uncordoned mid-drain", func() {
			target := workerNodes[1]
			nmpName := "e2e-uncordon-mid-drain"
			deployName := "e2e-uncordon-mid-drain-workload"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "uncordon", target)
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload with a slow preStop hook")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 120
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        lifecycle:
          preStop:
            sleep:
              seconds: 90
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e uncordon mid-drain test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainInProgress — eviction issued, pod is terminating")
			Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

			By("manually uncordoning the node mid-drain")
			cmd = exec.Command("kubectl", "uncordon", target)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying a DriftDetected warning event is emitted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "-A",
					"--field-selector", fmt.Sprintf("reason=DriftDetected,involvedObject.name=%s", nmpName),
					"-o", "name")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
			}).Should(Succeed())

			By("verifying the operator does not re-cordon the node")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("false"), BeEmpty()),
					"operator must not re-cordon a node after drift is detected")
			}, 15*time.Second, 3*time.Second).Should(Succeed())

			By("verifying managed-by annotation is removed after drift release")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(BeEmpty())
			}).Should(Succeed())
		})

		It("should not adopt nodes added to the cluster after a nodeSelector-based plan is created", func() {
			Expect(len(workerNodes)).To(BeNumerically(">=", 3),
				"this test requires at least 3 worker nodes")
			node1 := workerNodes[0]
			node2 := workerNodes[1]
			node3 := workerNodes[2]
			nmpName := "e2e-snapshot"
			testLabel := "maintenance.nmoo.io/e2e-snapshot-test"

			DeferCleanup(func() {
				for _, n := range []string{node1, node2, node3} {
					cmd := exec.Command("kubectl", "label", "node", n, testLabel+"-")
					_, _ = utils.Run(cmd)
				}
			})

			By("labeling two nodes to match the plan's nodeSelector")
			for _, n := range []string{node1, node2} {
				cmd := exec.Command("kubectl", "label", "node", n, testLabel+"=true")
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating NMP selecting nodes by label")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodeSelector:
    matchLabels:
      %s: "true"
  reason: "e2e snapshot test"
`, nmpName, testLabel)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for both nodes to be adopted and the snapshot to be persisted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", "jsonpath={.status.nodeSnapshotTaken}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"))
			}).Should(Succeed())

			By("verifying node1 and node2 are in the snapshot")
			for _, n := range []string{node1, node2} {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", "jsonpath={.status.resolvedNodes}")
				output, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring(n))
			}

			By("labeling a third node to match the selector — simulating a node added after plan creation")
			cmd = exec.Command("kubectl", "label", "node", node3, testLabel+"=true")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying node3 is never adopted across several reconcile cycles")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", node3,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(BeEmpty(), "node3 should not be adopted by the snapshot-locked plan")
			}, 20*time.Second, 3*time.Second).Should(Succeed())

			By("verifying node3 is absent from status.resolvedNodes")
			cmd = exec.Command("kubectl", "get", "nmp", nmpName,
				"-o", "jsonpath={.status.resolvedNodes}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).NotTo(ContainSubstring(node3))

			By("verifying node1 and node2 remain adopted throughout")
			for _, n := range []string{node1, node2} {
				cmd := exec.Command("kubectl", "get", "node", n,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(Equal(nmpName))
			}
		})

		It("should adopt and cordon nodes matching a nodeSelector", func() {
			Expect(len(workerNodes)).To(BeNumerically(">=", 2),
				"this test requires at least 2 worker nodes")
			node1 := workerNodes[0]
			node2 := workerNodes[1]
			nmpName := "e2e-nodeselector-cordon"
			testLabel := "maintenance.nmoo.io/e2e-nodeselector-test"

			DeferCleanup(func() {
				// Remove test labels first so no downstream test accidentally matches them,
				// then delete the plan (triggers uncordon via reconciler), then uncordon
				// directly as a safety net in case the reconciler doesn't finish in time.
				for _, n := range []string{node1, node2} {
					cmd := exec.Command("kubectl", "label", "node", n, testLabel+"-")
					_, _ = utils.Run(cmd)
				}
				cmd := exec.Command("kubectl", "delete", "nmp", nmpName, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
				for _, n := range []string{node1, node2} {
					cmd := exec.Command("kubectl", "uncordon", n)
					_, _ = utils.Run(cmd)
				}
			})

			By("labeling two nodes to match the plan's nodeSelector")
			for _, n := range []string{node1, node2} {
				cmd := exec.Command("kubectl", "label", "node", n, testLabel+"=true")
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating NMP with nodeSelector and cordon enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodeSelector:
    matchLabels:
      %s: "true"
  reason: "e2e nodeSelector cordon test"
  cordon:
    enabled: true
`, nmpName, testLabel)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the snapshot to be taken and both nodes adopted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", "jsonpath={.status.nodeSnapshotTaken}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"))
			}).Should(Succeed())

			By("verifying both nodes are adopted (managed-by annotation set)")
			for _, n := range []string{node1, node2} {
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "node", n,
						"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal(nmpName))
				}).Should(Succeed())
			}

			By("verifying both nodes are cordoned")
			for _, n := range []string{node1, node2} {
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "node", n,
						"-o", "jsonpath={.spec.unschedulable}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("true"))
				}).Should(Succeed())
			}

			By("deleting the plan and verifying nodes are uncordoned and annotation removed")
			cmd = exec.Command("kubectl", "delete", "nmp", nmpName)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			for _, n := range []string{node1, node2} {
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "node", n,
						"-o", "jsonpath={.spec.unschedulable}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Or(Equal("false"), BeEmpty()))
				}).Should(Succeed())

				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "node", n,
						"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(BeEmpty())
				}).Should(Succeed())
			}
		})

		It("should mark DrainTimedOut when drain does not complete within timeoutMinutes", func() {
			target := workerNodes[0]
			nmpName := "e2e-drain-timeout"
			deployName := "e2e-drain-timeout-workload"

			DeferCleanup(func() {
				// Delete deployment first so the controller stops recreating the pod before we force-delete it.
				cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "pods", "-n", "default",
					"-l", fmt.Sprintf("app=%s", deployName),
					"--grace-period=0", "--force", "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload with a very long preStop hook that will outlast the timeout")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 300
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        lifecycle:
          preStop:
            sleep:
              seconds: 240
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with a 1-minute drain timeout")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e drain timeout test"
  cordon:
    enabled: true
  drain:
    enabled: true
    timeoutMinutes: 1
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainTimedOut condition (allow extra time for cordon + eviction + 1m deadline)")
			Eventually(nmpCondition(nmpName, "DrainTimedOut"), 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying DrainInProgress is False and DrainSucceeded is False")
			cmd = exec.Command("kubectl", "get", "nmp", nmpName,
				"-o", `jsonpath={.status.conditions[?(@.type=="DrainInProgress")].status}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("False"))

			cmd = exec.Command("kubectl", "get", "nmp", nmpName,
				"-o", `jsonpath={.status.conditions[?(@.type=="DrainSucceeded")].status}`)
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("False"))

			By("verifying the operator does not retry after timeout — DrainTimedOut stays True")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", `jsonpath={.status.conditions[?(@.type=="DrainTimedOut")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 15*time.Second, 3*time.Second).Should(Succeed())
		})

		It("should detect pods stuck in Terminating beyond their grace period and report DrainBlocked with StuckTerminating issues", func() {
			target := workerNodes[0]
			nmpName := "e2e-stuck-terminating"
			podName := "e2e-stuck-term-pod"

			DeferCleanup(func() {
				// Remove the finalizer so the pod can actually terminate.
				cmd := exec.Command("kubectl", "patch", "pod", podName, "-n", "default",
					"--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
				_, _ = utils.Run(cmd)
				// Wait for the pod to be fully gone so it doesn't contaminate the next test
				// that uses this node — the pod has terminationGracePeriodSeconds: 1 so
				// deletion should be near-instant once the finalizer is removed.
				cmd = exec.Command("kubectl", "delete", "pod", podName, "-n", "default",
					"--ignore-not-found=true", "--wait=true", "--timeout=30s")
				_, _ = utils.Run(cmd)
			})

			By("creating a pod with a finalizer that prevents deletion after eviction")
			podYAML := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  finalizers:
    - maintenance.nmoo.io/e2e-test
spec:
  nodeName: %s
  terminationGracePeriodSeconds: 1
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.9
`, podName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(podYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for pod to be Running")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName, "-n", "default",
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"))
			}).Should(Succeed())

			By("creating NMP with cordon, drain, and force=true so the bare pod is evictable")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e stuck terminating test"
  cordon:
    enabled: true
  drain:
    enabled: true
    options:
      force: true
      ignoreDaemonSets: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainInProgress — eviction was issued and pod entered Terminating state")
			Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

			By("verifying the pod has a DeletionTimestamp set by the eviction")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName, "-n", "default",
					"-o", "jsonpath={.metadata.deletionTimestamp}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
			}).Should(Succeed())

			// The operator classifies a pod as StuckTerminating once its DeletionTimestamp is
			// older than terminationGracePeriodSeconds + 60s.
			By("waiting for DrainBlocked — pod exceeds grace period + 60s stuck-terminating buffer")
			Eventually(nmpCondition(nmpName, "DrainBlocked"), 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying DrainInProgress is False while the stuck pod blocks drain")
			// DrainInProgress may briefly coexist with DrainBlocked if other pods were
			// terminating alongside the stuck pod — use Eventually to wait for it to settle.
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", `jsonpath={.status.conditions[?(@.type=="DrainInProgress")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("False"))
			}).Should(Succeed())

			By("verifying the node status contains a StuckTerminating issue referencing the stuck pod")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName, "-o", "json")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(`"StuckTerminating"`))
				g.Expect(output).To(ContainSubstring(podName))
			}).Should(Succeed())

			By("verifying a DrainBlocked warning event is emitted for the stuck pod")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "-A",
					"--field-selector", fmt.Sprintf("reason=DrainBlocked,involvedObject.name=%s", nmpName),
					"-o", "name")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
			}).Should(Succeed())

			By("removing the finalizer to allow the stuck pod to be deleted")
			cmd = exec.Command("kubectl", "patch", "pod", podName, "-n", "default",
				"--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainSucceeded once the stuck pod is gone")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should honour podTerminationGracePeriodSeconds by overriding the pod's own grace period", func() {
			target := workerNodes[1]
			nmpName := "e2e-grace-period"
			deployName := "e2e-grace-period-workload"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload whose preStop hook is far longer than the grace period override")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 90
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        lifecycle:
          preStop:
            sleep:
              seconds: 60
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with a 5-second pod termination grace period override")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e grace period override test"
  cordon:
    enabled: true
  drain:
    enabled: true
    options:
      podTerminationGracePeriodSeconds: 5
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying drain completes within 45s — faster than the 60s preStop would normally take, proving the override is applied")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 45*time.Second, 2*time.Second).Should(Succeed())

			By("verifying no pods remain on the target node")
			cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
				"-l", fmt.Sprintf("app=%s", deployName),
				"--field-selector", fmt.Sprintf("spec.nodeName=%s", target),
				"-o", "name")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty())
		})

		It("should set in-maintenance label on node adoption and remove it on plan deletion", func() {
			target := workerNodes[0]
			nmpName := "e2e-in-maintenance-label"

			By("creating NMP targeting the node")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e in-maintenance label test"
`, nmpName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for node to be adopted (managed-by annotation set)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(nmpName))
			}).Should(Succeed())

			By("verifying in-maintenance label is set on the node")
			cmd = exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.labels.maintenance\.nmoo\.io/in-maintenance}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("true"))

			By("deleting the NMP and waiting for node release")
			cmd = exec.Command("kubectl", "delete", "nmp", nmpName, "--wait=true", "--timeout=60s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying in-maintenance label is removed after plan deletion")
			cmd = exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.labels.maintenance\.nmoo\.io/in-maintenance}`)
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(BeEmpty())
		})

		It("should report DrainBlocked with NodeNotReady issue when a managed node goes NotReady, and resume drain after recovery", func() {
			target := workerNodes[1]
			nmpName := "e2e-notready-recovery"
			deployName := "e2e-notready-workload"

			DeferCleanup(func() {
				cmd := exec.Command("docker", "exec", target, "systemctl", "start", "kubelet")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload with a container-side preStop hook on the target node")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 120
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: workload
        image: busybox
        command: ["sleep", "infinity"]
        lifecycle:
          preStop:
            exec:
              command: ["sleep", "60"]
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on the target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e not-ready recovery test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainInProgress — eviction issued, preStop hook running in container")
			Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

			By("stopping the kubelet on the target node to simulate a node going NotReady")
			cmd = exec.Command("docker", "exec", target, "systemctl", "stop", "kubelet")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the node to be reported as NotReady (Unknown or False)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("False"), Equal("Unknown")))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("waiting for DrainBlocked condition due to NotReady node")
			Eventually(nmpCondition(nmpName, "DrainBlocked"), 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying NodeNotReady issue is present in the NMP node status")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName, "-o", "json")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(`"NodeNotReady"`))
			}).Should(Succeed())

			By("verifying NotReadySince is set in NMP status for the target node")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", fmt.Sprintf(`jsonpath={.status.nodes[?(@.name=="%s")].notReadySince}`, target))
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
			}).Should(Succeed())

			By("verifying no yield event was emitted — node has been NotReady well under the 300s threshold")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "-A",
					"--field-selector", fmt.Sprintf("reason=NodeNotReady,involvedObject.name=%s", nmpName),
					"-o", "jsonpath={.items[*].message}")
				output, _ := utils.Run(cmd)
				g.Expect(output).NotTo(ContainSubstring("yielding"))
			}, 5*time.Second, 1*time.Second).Should(Succeed())

			By("restarting the kubelet to simulate node recovery")
			cmd = exec.Command("docker", "exec", target, "systemctl", "start", "kubelet")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the node to return to Ready")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", target,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying DrainBlocked clears after the node recovers")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", `jsonpath={.status.conditions[?(@.type=="DrainBlocked")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("False"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying NotReadySince is cleared after the node recovers — preventing premature yield on a future flip")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", fmt.Sprintf(`jsonpath={.status.nodes[?(@.name=="%s")].notReadySince}`, target))
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(BeEmpty())
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("waiting for drain to complete after node recovery")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 4*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should wait for pods to fully terminate before reporting drain complete", func() {
			target := workerNodes[4]
			nmpName := "e2e-slow-term"
			deployName := "e2e-slow-term-workload"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("deploying a workload with a slow preStop hook on the target node")
			deployYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: 40
      nodeSelector:
        kubernetes.io/hostname: %s
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        lifecycle:
          preStop:
            sleep:
              seconds: 30
`, deployName, deployName, deployName, target)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for workload pod to be Running on target node")
			Eventually(waitForPodRunning(fmt.Sprintf("app=%s", deployName), target)).Should(Succeed())

			By("creating NMP with cordon and drain enabled")
			nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e slow termination test"
  cordon:
    enabled: true
  drain:
    enabled: true
`, nmpName, target)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DrainInProgress — eviction has been issued but pod is still terminating")
			Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

			By("verifying drain does not report success while pod is still terminating")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", `jsonpath={.status.conditions[?(@.type=="DrainSucceeded")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal("True"))
			}, 10*time.Second, 2*time.Second).Should(Succeed())

			By("waiting for drain to complete once pod has fully terminated")
			Eventually(nmpCondition(nmpName, "DrainSucceeded"), 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the node is empty")
			cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
				"-l", fmt.Sprintf("app=%s", deployName),
				"--field-selector", fmt.Sprintf("spec.nodeName=%s", target),
				"-o", "name")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty())
		})
	})

	webhookSuite()
})

func nmpCondition(nmpName, condType string) func(g Gomega) {
	return func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "nmp", nmpName,
			"-o", fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].status}`, condType))
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"))
	}
}

func waitForPodRunning(label, targetNode string) func(g Gomega) {
	return func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-n", "default",
			"-l", label,
			"--field-selector", fmt.Sprintf("spec.nodeName=%s,status.phase=Running", targetNode),
			"-o", "name")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
	}
}

func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	if err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), 0o644); err != nil {
		return "", err
	}

	var out string
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace, serviceAccountName,
		), "-f", tokenRequestFile)
		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())
		var token tokenRequest
		g.Expect(json.Unmarshal(output, &token)).To(Succeed())
		out = token.Status.Token
	}).Should(Succeed())

	return out, nil
}

func getMetricsOutput() (string, error) {
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
