//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/gomega"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/test/utils"
)

const (
	namespace              = "node-maintenance-orchestrator-system"
	serviceAccountName     = "node-maintenance-orchestrator-controller-manager"
	metricsServiceName     = "node-maintenance-orchestrator-ctrl-manager-metrics-service"
	metricsRoleBindingName = "node-maintenance-orchestrator-metrics-binding"
	webhookConfigName      = "node-maintenance-orchestrator-validating-webhook-configuration"
	webhookSecretName      = "node-maintenance-orchestrator-webhook-cert"
)

// Package-level state shared across all Describe blocks in the suite.
var (
	controllerPodName string
	workerNodes       []string
)

// nmpCondition returns an Eventually-compatible func that passes when the named
// condition on an NMP is True.
func nmpCondition(nmpName, condType string) func(g Gomega) {
	return func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "nmp", nmpName,
			"-o", fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].status}`, condType))
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"))
	}
}

// waitForPodRunning returns an Eventually-compatible func that passes when at
// least one pod matching label is Running on targetNode.
func waitForPodRunning(label, targetNode string) func(g Gomega) {
	return func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-n", "default",
			"-l", label,
			"--field-selector", fmt.Sprintf("spec.nodeName=%s,status.phase=Running", targetNode),
			"-o", "name")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).NotTo(BeEmpty())
	}
}

// serviceAccountToken returns a short-lived token for the controller-manager SA.
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
