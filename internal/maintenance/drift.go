package maintenance

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

const (
	DriftReasonManualUncordon = "ManualUncordon"
	// DriftReasonExternalCordon is set when a node managed by a plan with cordon
	// disabled is cordoned by an external actor (autoscaler, another operator, etc.).
	// Ownership is retained so the plan can still coordinate drain and other operations;
	// the operator simply does not fight the external cordon.
	//
	// Unlike ManualUncordon, the node stays in res.Stable on every reconcile,
	// so the drift event would re-fire each pass. The EventRecorder deduplicates
	// over ~10 minutes, so it won't spam badly, but it's worth noting for later.
	// TODO: fix the above issue.
	DriftReasonExternalCordon = "ExternalCordon"

	// DriftReasonMaintenanceComplete is returned when a node that has already reached
	// ReadyForMaintenance=true is uncordoned. This is the expected final step of the
	// maintenance workflow (user uncordons the node to return it to service) and is
	// not a drift situation. A MaintenanceComplete Normal event is emitted instead of
	// a DriftDetected warning, and ownership is released cleanly.
	DriftReasonMaintenanceComplete = "MaintenanceComplete"

	// ExternalDrain detection is intentionally not implemented: pod state alone cannot
	// distinguish operator-evicted from externally-evicted pods. The operator handles
	// this transparently — filterPodsForDrain finds no remaining pods and marks the node
	// complete. A future approach could track eviction ownership via pod annotations.
)

// DetectNodeDrift returns true when a stable node (owned, in desired set) has
// drifted from the expected cordon state. Three cases are detected:
//
//   - MaintenanceComplete: node was uncordoned after reaching ReadyForMaintenance.
//     This is the expected end of the maintenance workflow, not a drift situation.
//     Handled with a Normal event and clean ownership release.
//
//   - ManualUncordon: plan requires cordon, operator cordoned the node, but it is
//     now schedulable before maintenance was complete. Ownership is released.
//
//   - ExternalCordon: plan does not require cordon, but the node is unschedulable
//     due to an external actor. Ownership is retained; the operator skips the node.
func DetectNodeDrift(node *corev1.Node, plan *v1alpha1.NodeMaintenancePlan) (bool, string) {

	if node == nil || node.Annotations == nil {
		return false, ""
	}

	if node.Annotations[ManagedByAnnotation] != plan.Name {
		return false, ""
	}

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled
	operatorCordoned := node.Annotations[CordonedAnnotation] == "true"

	if cordonEnabled && operatorCordoned && !node.Spec.Unschedulable {
		// Node was uncordoned. Check whether maintenance was already complete —
		// if so this is the expected final step, not unwanted interference.
		for _, ns := range plan.Status.Nodes {
			if ns.Name == node.Name && ns.ReadyForMaintenance {
				return true, DriftReasonMaintenanceComplete
			}
		}
		return true, DriftReasonManualUncordon
	}

	if !cordonEnabled && !operatorCordoned && node.Spec.Unschedulable {
		return true, DriftReasonExternalCordon
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

// nodeCompletedMaintenance reports whether nodeName has already completed
// maintenance (readyForMaintenance=true in status). Used to prevent
// re-adoption after ReconcileDrift releases a completed node.
func nodeCompletedMaintenance(plan *v1alpha1.NodeMaintenancePlan, nodeName string) bool {
	for _, ns := range plan.Status.Nodes {
		if ns.Name == nodeName {
			return ns.ReadyForMaintenance
		}
	}
	return false
}
