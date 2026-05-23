//go:build e2e
// +build e2e

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

// namespace where the project is deployed in
const namespace = "node-maintenance-orchestrator-system"

// serviceAccountName created for the project
const serviceAccountName = "node-maintenance-orchestrator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "node-maintenance-orchestrator-ctrl-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "node-maintenance-orchestrator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
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

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
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
				// Get the name of the controller-manager pod
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

				// Validate the pod's status
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
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
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

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput, err := getMetricsOutput()
		// Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})

	Context("NodeMaintenancePlan", Ordered, func() {
		var workerNodes []string

		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)

		BeforeAll(func() {
			By("listing worker nodes")
			cmd := exec.Command("kubectl", "get", "nodes",
				"--selector=!node-role.kubernetes.io/control-plane",
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "failed to list worker nodes")
			workerNodes = strings.Fields(strings.TrimSpace(output))
			Expect(len(workerNodes)).To(BeNumerically(">=", 2),
				"e2e tests require at least 2 worker nodes")
		})

		AfterEach(func() {
			By("deleting all NMPs")
			cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		// nmpCondition polls until a named condition on an NMP is True.
		nmpCondition := func(nmpName, condType string) func(g Gomega) {
			return func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nmp", nmpName,
					"-o", fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].status}`, condType))
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}
		}

		// waitForPodRunning polls until at least one pod matching the label is Running on the target node.
		waitForPodRunning := func(label, targetNode string) func(g Gomega) {
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

		It("should not steal ownership when two plans target overlapping node sets", func() {
			sharedNode := workerNodes[0]
			exclusiveNode := workerNodes[1]
			planA := "e2e-conflict-a"
			planB := "e2e-conflict-b"

			By("creating plan A to own the shared node")
			nmpAYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "conflict test plan A"
`, planA, sharedNode)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpAYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for plan A to adopt the shared node")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", sharedNode,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(planA))
			}).Should(Succeed())

			By("creating plan B targeting the shared node and an additional exclusive node")
			nmpBYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
    - %s
  reason: "conflict test plan B"
`, planB, sharedNode, exclusiveNode)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpBYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for plan B to adopt its exclusive node")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", exclusiveNode,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(planB))
			}).Should(Succeed())

			By("verifying the shared node ownership is never transferred to plan B")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", sharedNode,
					"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(planA))
			}, 15*time.Second, 3*time.Second).Should(Succeed())

			By("verifying an OwnershipConflict warning event was emitted for plan B")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "-A",
					"--field-selector", fmt.Sprintf("reason=OwnershipConflict,involvedObject.name=%s", planB),
					"-o", "name")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
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

		It("should mark DrainTimedOut when drain does not complete within timeoutMinutes", func() {
			target := workerNodes[0]
			nmpName := "e2e-drain-timeout"
			deployName := "e2e-drain-timeout-workload"

			DeferCleanup(func() {
				// Force-delete so a 300s-grace pod does not slow down test cleanup.
				cmd := exec.Command("kubectl", "delete", "pods", "-n", "default",
					"-l", fmt.Sprintf("app=%s", deployName),
					"--grace-period=0", "--force", "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
					"--ignore-not-found=true", "--wait=false")
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
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
