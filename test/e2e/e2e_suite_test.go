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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/test/utils"
)

var (
	projectImage = "example.com/node-maintenance-orchestrator:v0.0.1"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting node-maintenance-orchestrator integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
	output, err := utils.Run(cmd)
	_, _ = fmt.Fprintf(GinkgoWriter, "docker-build output:\n%s\n", output)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager image into Kind")
	err = utils.LoadImageToKindClusterWithName(projectImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load image into Kind")

	By("creating manager namespace")
	cmd = exec.Command("kubectl", "create", "ns", namespace)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

	By("labeling namespace with restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
		"pod-security.kubernetes.io/enforce=restricted")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("installing CRDs")
	cmd = exec.Command("make", "install")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

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
	Expect(len(workerNodes)).To(BeNumerically(">=", 2),
		"e2e tests require at least 2 worker nodes")

	By("waiting for controller pods to be running")
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

var _ = AfterSuite(func() {
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

	// Skip cert-manager teardown: not used by this operator (self-signed certs).
	_ = os.Getenv("CERT_MANAGER_INSTALL_SKIP")
})

// ReportAfterEach collects debug info on any test failure.
var _ = ReportAfterEach(func(report SpecReport) {
	if !report.Failed() {
		return
	}

	By("Fetching controller manager pod logs")
	cmd := exec.Command("kubectl", "logs", "-l", "control-plane=controller-manager",
		"-n", namespace, "--all-containers=true", "--tail=100")
	if logs, err := utils.Run(cmd); err == nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n%s\n", logs)
	}

	By("Fetching Kubernetes events")
	cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
	if events, err := utils.Run(cmd); err == nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s\n", events)
	}
})
