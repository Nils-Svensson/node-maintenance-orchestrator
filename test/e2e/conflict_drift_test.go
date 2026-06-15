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

var _ = Describe("ConflictAndDrift", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	AfterEach(func() {
		cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
		_, _ = utils.Run(cmd)
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

})
