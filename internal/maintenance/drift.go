package maintenance

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

const DriftReasonManualUncordon = "ManualUncordon"

// DetectNodeDrift returns true when a stable node (owned, in desired set) has
// been manually uncordoned while the plan still requires cordon.
func DetectNodeDrift(node *corev1.Node, plan *v1alpha1.NodeMaintenancePlan) (bool, string) {

	if node == nil || node.Annotations == nil {
		return false, ""
	}

	if node.Annotations[ManagedByAnnotation] != plan.Name {
		return false, ""
	}

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled

	if cordonEnabled && !node.Spec.Unschedulable {
		return true, DriftReasonManualUncordon
	}

	return false, ""
}

// GetNodeDriftState returns the drift state recorded in the plan status for nodeName.
func GetNodeDriftState(plan *v1alpha1.NodeMaintenancePlan, nodeName string) (drifted bool, reason string) {
	for _, ns := range plan.Status.Nodes {
		if ns.Name == nodeName {
			return ns.Drifted, ns.DriftReason
		}
	}
	return false, ""
}
