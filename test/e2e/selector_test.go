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

var _ = Describe("NodeSelector", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	AfterEach(func() {
		cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
		_, _ = utils.Run(cmd)
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
})
