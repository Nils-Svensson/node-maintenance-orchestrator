//go:build e2e
// +build e2e

package e2e

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/test/utils"
)

func webhookSuite() {
	Context("Webhook", Ordered, func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)

		AfterEach(func() {
			cmd := exec.Command("kubectl", "delete", "nmp", "--all", "--wait=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

	It("should reject creation of a plan with nodes that do not exist in the cluster", func() {
		nmpName := "e2e-webhook-nonexistent"

		DeferCleanup(func() {
			cmd := exec.Command("kubectl", "delete", "nmp", nmpName, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		By("attempting to create NMP referencing a non-existent node")
		nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - node-does-not-exist-xyzzy
  reason: "e2e webhook rejection test"
`, nmpName)

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			output, err := utils.Run(cmd)
			if err == nil {
				// Webhook was lenient (failurePolicy: Ignore kicked in) — clean up and retry.
				_ = exec.Command("kubectl", "delete", "nmp", nmpName, "--ignore-not-found=true").Run()
				g.Expect("webhook accepted request").To(Equal("webhook should have denied request"))
				return
			}
			g.Expect(output).To(ContainSubstring("does not exist"))
		}).Should(Succeed())
	})

	It("should reject creation of a plan targeting a node already owned by another plan", func() {
		ownerPlan := "e2e-webhook-owner"
		conflictPlan := "e2e-webhook-conflict"
		target := workerNodes[0]

		DeferCleanup(func() {
			for _, name := range []string{ownerPlan, conflictPlan} {
				cmd := exec.Command("kubectl", "delete", "nmp", name, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			}
		})

		By("creating plan A to own the target node")
		ownerYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e webhook owner plan"
`, ownerPlan, target)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(ownerYAML)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for plan A to adopt the node")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(ownerPlan))
		}).Should(Succeed())

		By("attempting to create a second plan targeting the same node")
		conflictYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e webhook conflict plan"
`, conflictPlan, target)

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(conflictYAML)
			output, err := utils.Run(cmd)
			if err == nil {
				// Webhook was lenient — clean up and retry.
				_ = exec.Command("kubectl", "delete", "nmp", conflictPlan, "--ignore-not-found=true").Run()
				g.Expect("webhook accepted request").To(Equal("webhook should have denied request"))
				return
			}
			g.Expect(output).To(ContainSubstring("already owned by"))
		}).Should(Succeed())
	})

	It("should reject creation of a nodeSelector plan whose selector matches a node already owned by another plan", func() {
		ownerPlan := "e2e-webhook-selector-owner"
		conflictPlan := "e2e-webhook-selector-conflict"
		target := workerNodes[0]
		testLabel := "maintenance.nmoo.io/e2e-webhook-selector-test"

		DeferCleanup(func() {
			cmd := exec.Command("kubectl", "label", "node", target, testLabel+"-")
			_, _ = utils.Run(cmd)
			for _, name := range []string{ownerPlan, conflictPlan} {
				cmd := exec.Command("kubectl", "delete", "nmp", name, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			}
		})

		By("labeling the target node so the conflict plan's selector will match it")
		cmd := exec.Command("kubectl", "label", "node", target, testLabel+"=true")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("creating plan A to own the target node")
		ownerYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - %s
  reason: "e2e webhook selector owner plan"
`, ownerPlan, target)
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(ownerYAML)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for plan A to adopt the node")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "node", target,
				"-o", `jsonpath={.metadata.annotations.maintenance\.nmoo\.io/managed-by}`)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(ownerPlan))
		}).Should(Succeed())

		By("attempting to create a nodeSelector plan whose selector resolves to the already-owned node")
		conflictYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodeSelector:
    matchLabels:
      %s: "true"
  reason: "e2e webhook nodeSelector conflict plan"
`, conflictPlan, testLabel)

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(conflictYAML)
			output, err := utils.Run(cmd)
			if err == nil {
				_ = exec.Command("kubectl", "delete", "nmp", conflictPlan, "--ignore-not-found=true").Run()
				g.Expect("webhook accepted request").To(Equal("webhook should have denied request"))
				return
			}
			g.Expect(output).To(ContainSubstring("already owned by"))
		}).Should(Succeed())
	})

	It("should automatically rotate the CA cert when it is within the renewal window", func() {
		By("reading the current caBundle from the ValidatingWebhookConfiguration")
		cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
			webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
		originalCABundle, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(originalCABundle)).NotTo(BeEmpty())

		By("generating a near-expiry CA cert (29 days remaining, inside the 30-day renewal window)")
		caCert, caKey, err := utils.GenerateExpiredCA(29)
		Expect(err).NotTo(HaveOccurred())

		By("patching the webhook cert Secret to inject the near-expiry CA")
		patch := fmt.Sprintf(`{"data":{"ca.crt":"%s","ca.key":"%s"}}`,
			base64.StdEncoding.EncodeToString(caCert),
			base64.StdEncoding.EncodeToString(caKey))
		cmd = exec.Command("kubectl", "patch", "secret", webhookSecretName,
			"-n", namespace, "--type=merge", "-p", patch)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("rolling out the controller-manager to trigger cert bootstrapping")
		cmd = exec.Command("kubectl", "rollout", "restart",
			"deployment/node-maintenance-orchestrator-controller-manager", "-n", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the rollout to complete")
		cmd = exec.Command("kubectl", "rollout", "status",
			"deployment/node-maintenance-orchestrator-controller-manager",
			"-n", namespace, "--timeout=3m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the caBundle to be updated with the new rotated cert")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
				webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			newCABundle, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(newCABundle)).NotTo(BeEmpty())
			g.Expect(newCABundle).NotTo(Equal(originalCABundle), "caBundle should have changed after rotation")
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying webhook is still functioning — rejection of invalid plan works with new cert")
		nmpName := "e2e-webhook-post-rotation"
		nmpYAML := fmt.Sprintf(`
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: %s
spec:
  nodes:
    - node-does-not-exist-post-rotation
  reason: "e2e cert rotation verification"
`, nmpName)

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nmpYAML)
			output, err := utils.Run(cmd)
			if err == nil {
				_ = exec.Command("kubectl", "delete", "nmp", nmpName, "--ignore-not-found=true").Run()
				g.Expect("webhook accepted request").To(Equal("webhook should have denied request"))
				return
			}
			g.Expect(output).To(ContainSubstring("does not exist"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
	})
}
