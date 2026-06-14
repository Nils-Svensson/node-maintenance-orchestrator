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

var _ = Describe("Health", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	AfterEach(func() {
		cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
		_, _ = utils.Run(cmd)
	})

	It("should report DrainBlocked with NodeNotReady issue when a managed node goes NotReady, and resume drain after recovery", func() {
		target := workerNodes[0]
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

		By("waiting for the node to be reported as NotReady")
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

		By("verifying NotReadySince is cleared after node recovery")
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
})
