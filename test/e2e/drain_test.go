//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/test/utils"
)

var _ = Describe("Drain", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	AfterEach(func() {
		cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
		_, _ = utils.Run(cmd)
	})

	It("should report DrainBlocked when a PodDisruptionBudget prevents eviction, then complete after PDB is removed", func() {
		target := workerNodes[0]
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

		By("verifying no pods remain on the target node")
		cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
			"-l", fmt.Sprintf("app=%s", deployName),
			"--field-selector", fmt.Sprintf("spec.nodeName=%s", target),
			"-o", "name")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(output)).To(BeEmpty())
	})

	It("should block drain for an uncontrolled pod, then drain successfully after enabling force", func() {
		target := workerNodes[1]
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

		By("creating NMP with drain enabled and force disabled")
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

		By("patching NMP to enable force")
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
		Expect(err).NotTo(HaveOccurred())

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

	It("should mark DrainTimedOut when drain does not complete within timeoutMinutes", func() {
		target := workerNodes[1]
		nmpName := "e2e-drain-timeout"
		deployName := "e2e-drain-timeout-workload"

		DeferCleanup(func() {
			cmd := exec.Command("kubectl", "delete", "pods", "-n", "default",
				"-l", fmt.Sprintf("app=%s", deployName),
				"--grace-period=0", "--force", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "deployment", deployName, "-n", "default",
				"--ignore-not-found=true", "--wait=false")
			_, _ = utils.Run(cmd)
		})

		By("deploying a workload with a preStop hook that outlasts the drain timeout")
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

		By("waiting for DrainTimedOut condition")
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

		By("verifying DrainTimedOut stays True — operator does not retry after timeout")
		Consistently(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "nmp", nmpName,
				"-o", `jsonpath={.status.conditions[?(@.type=="DrainTimedOut")].status}`)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("True"))
		}, 15*time.Second, 3*time.Second).Should(Succeed())
	})

	It("should detect pods stuck in Terminating beyond their grace period and report DrainBlocked", func() {
		target := workerNodes[0]
		nmpName := "e2e-stuck-terminating"
		podName := "e2e-stuck-term-pod"

		DeferCleanup(func() {
			cmd := exec.Command("kubectl", "patch", "pod", podName, "-n", "default",
				"--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "pod", podName, "-n", "default",
				"--ignore-not-found=true", "--wait=false")
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

		By("creating NMP with force=true so the bare pod is evictable")
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

		By("waiting for DrainInProgress — eviction issued, pod entering Terminating state")
		Eventually(nmpCondition(nmpName, "DrainInProgress")).Should(Succeed())

		By("verifying the pod has a DeletionTimestamp set")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pod", podName, "-n", "default",
				"-o", "jsonpath={.metadata.deletionTimestamp}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(output)).NotTo(BeEmpty())
		}).Should(Succeed())

		By("waiting for DrainBlocked — pod exceeds grace period + stuck-terminating buffer")
		Eventually(nmpCondition(nmpName, "DrainBlocked"), 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying the NMP node status contains a StuckTerminating issue")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "nmp", nmpName, "-o", "json")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(ContainSubstring(`"StuckTerminating"`))
			g.Expect(output).To(ContainSubstring(podName))
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

		By("verifying drain completes within 45s — faster than the 60s preStop, proving override is applied")
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
		target := workerNodes[0]
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

		By("waiting for DrainInProgress — eviction issued, pod still terminating")
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
