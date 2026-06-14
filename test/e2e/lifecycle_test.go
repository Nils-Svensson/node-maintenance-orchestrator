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

var _ = Describe("Lifecycle", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	AfterEach(func() {
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
			cmd := exec.Command("kubectl", "get", "node", target, "-o", "jsonpath={.spec.unschedulable}")
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
			cmd := exec.Command("kubectl", "get", "node", target, "-o", "jsonpath={.spec.unschedulable}")
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

		Eventually(nmpCondition(nmpName, "DrainSucceeded")).Should(Succeed())

		By("verifying no pods remain on the target node")
		cmd = exec.Command("kubectl", "get", "pods", "-n", "default",
			"-l", fmt.Sprintf("app=%s", deployName),
			"--field-selector", fmt.Sprintf("spec.nodeName=%s", target), "-o", "name")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(output)).To(BeEmpty())
	})

	It("should set in-maintenance label on node adoption and remove it on plan deletion", func() {
		target := workerNodes[0]
		nmpName := "e2e-in-maintenance-label"

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

		By("waiting for node adoption")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(nmpName))
		}).Should(Succeed())

		By("verifying in-maintenance label is set")
		cmd = exec.Command("kubectl", "get", "node", target,
			"-o", `jsonpath={.metadata.labels.maintenance\.nmoo\.io/in-maintenance}`)
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("true"))

		By("deleting the NMP")
		cmd = exec.Command("kubectl", "delete", "nmp", nmpName, "--wait=true", "--timeout=60s")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying in-maintenance label is removed")
		cmd = exec.Command("kubectl", "get", "node", target,
			"-o", `jsonpath={.metadata.labels.maintenance\.nmoo\.io/in-maintenance}`)
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(BeEmpty())
	})
})
