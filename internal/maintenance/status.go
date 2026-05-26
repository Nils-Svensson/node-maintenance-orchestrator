// status.go is responsible for constructing and updating the
// NodeMaintenancePlan status.
//
// It aggregates data from preview, execution, and node state into a
// structured representation, including per-node status, issues, and
// high-level conditions.
//
// This layer separates status computation from reconciliation logic,
// improving readability and maintainability.

package maintenance

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

// recomputePlanSummaries updates Phase, ReadySummary, DrainProgress,
// DrainingNodeCount, and BlockedNodeCount from the current per-node status.
// Call it whenever plan.Status.Nodes changes.
func recomputePlanSummaries(plan *v1alpha1.NodeMaintenancePlan) {
	var ready, draining, blocked, totalProgress int32
	var anyDrifted bool
	anyDrifted = false
	total := int32(len(plan.Status.Nodes))

	for _, ns := range plan.Status.Nodes {
		totalProgress += ns.DrainProgress
		if ns.ReadyForMaintenance {
			ready++
		} else if ns.TotalPods > 0 {
			draining++
		}
		if ns.BlockedPods > 0 {
			blocked++
		}
		if ns.Drifted {
			anyDrifted = true
		}
	}

	plan.Status.ReadySummary = fmt.Sprintf("%d/%d", ready, total)
	plan.Status.DrainingNodeCount = fmt.Sprintf("%d/%d", draining, total)
	plan.Status.BlockedNodeCount = fmt.Sprintf("%d/%d", blocked, total)
	plan.Status.Drifted = &anyDrifted
	if total > 0 {
		plan.Status.DrainProgress = fmt.Sprintf("%d%%", totalProgress/total)
	} else {
		plan.Status.DrainProgress = "0%"
	}
	plan.Status.Phase = computePhase(plan)
}

func computePhase(plan *v1alpha1.NodeMaintenancePlan) string {
	switch {
	case isConditionTrue(plan, v1alpha1.ConditionDrainTimedOut):
		return "TimedOut"
	case isConditionTrue(plan, v1alpha1.ConditionConflict):
		return "Conflict"
	case plan.Status.AllNodesReadyForMaintenance:
		return "Ready"
	case isConditionTrue(plan, v1alpha1.ConditionDrainBlocked):
		return "Blocked"
	case isConditionTrue(plan, v1alpha1.ConditionDrainInProgress):
		return "Draining"
	case isConditionTrue(plan, v1alpha1.ConditionCordoned):
		return "Cordoned"
	case isConditionTrue(plan, v1alpha1.ConditionScheduled):
		return "Scheduled"
	case isConditionTrue(plan, v1alpha1.ConditionNodesSelected):
		return "Adopted"
	default:
		return "Pending"
	}
}

func (s *MaintenanceService) UpdateStatus(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	// Index existing per-node status so drain counters and progress survive
	// across reconcile passes. UpdateStatus only owns the fields below; all
	// other fields are preserved from the previous status entry.
	existing := make(map[string]v1alpha1.NodeStatus, len(plan.Status.Nodes))
	for _, ns := range plan.Status.Nodes {
		existing[ns.Name] = ns
	}

	statuses := make([]v1alpha1.NodeStatus, 0, len(res.All))

	for _, node := range res.All {

		drifted, reason := DetectNodeDrift(node, plan)

		// If not currently detectable (annotation already removed by a prior
		// ReconcileDrift), carry forward the drift state from the previous status
		// so the node remains marked drifted until removed from the spec.
		if !drifted {
			drifted, reason = GetNodeDriftState(plan, node.Name)
		}

		// MaintenanceComplete is an expected lifecycle transition, not a drift
		// condition. Don't persist it as Drifted in status.
		if reason == DriftReasonMaintenanceComplete {
			drifted = false
			reason = ""
		}

		// Start from the previous entry so drain counters are preserved, then
		// overwrite only the fields this function is responsible for.
		ns := existing[node.Name]
		ns.Name = node.Name
		ns.Cordoned = node.Spec.Unschedulable
		ns.Drifted = drifted
		ns.DriftReason = reason

		statuses = append(statuses, ns)
	}

	original := plan.DeepCopy()

	plan.Status.Nodes = statuses
	plan.Status.NodeCount = int32(len(statuses))

	// NodesSelected — at least one node is under management.
	if len(statuses) > 0 {
		setCondition(plan, v1alpha1.ConditionNodesSelected, metav1.ConditionTrue,
			"NodesAdopted", fmt.Sprintf("%d node(s) under management", len(statuses)))
	} else {
		setCondition(plan, v1alpha1.ConditionNodesSelected, metav1.ConditionFalse,
			"NoNodes", "No nodes selected or all nodes released")
	}

	// Cordoned — cordon is enabled and every non-drifted managed node is unschedulable.
	// Drifted nodes are excluded: the operator has already released them and they
	// should not prevent the plan from reaching the Cordoned phase.
	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled
	allCordoned := cordonEnabled && len(statuses) > 0
	nonDriftedCount := 0
	for _, ns := range statuses {
		if ns.Drifted {
			continue
		}
		nonDriftedCount++
		if !ns.Cordoned {
			allCordoned = false
		}
	}
	if nonDriftedCount == 0 {
		allCordoned = false
	}
	if allCordoned {
		setCondition(plan, v1alpha1.ConditionCordoned, metav1.ConditionTrue,
			"AllNodesCordoned", "All managed nodes are unschedulable")
	} else {
		setCondition(plan, v1alpha1.ConditionCordoned, metav1.ConditionFalse,
			"NotAllCordoned", "Not all managed nodes are unschedulable")
	}

	// Conflict — one or more nodes are already owned by another plan.
	if len(res.Conflicting) > 0 {
		setCondition(plan, v1alpha1.ConditionConflict, metav1.ConditionTrue,
			"NodeConflict", fmt.Sprintf("%d node(s) already owned by another plan", len(res.Conflicting)))
	} else {
		setCondition(plan, v1alpha1.ConditionConflict, metav1.ConditionFalse,
			"NoConflict", "No conflicting node ownership")
	}

	// Scheduled — nodes selected but cordon start is still in the future.
	isScheduled := !allCordoned &&
		plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled &&
		plan.Spec.Cordon.StartAt != nil &&
		plan.Spec.Cordon.StartAt.Time.After(s.clock.Now())
	if isScheduled {
		setCondition(plan, v1alpha1.ConditionScheduled, metav1.ConditionTrue,
			"ScheduledInFuture", fmt.Sprintf("cordon scheduled for %s",
				plan.Spec.Cordon.StartAt.UTC().Format(time.RFC3339)))
	} else {
		setCondition(plan, v1alpha1.ConditionScheduled, metav1.ConditionFalse,
			"NotScheduled", "No pending scheduled activation")
	}

	recomputePlanSummaries(plan)

	if res.SnapshotNodes != nil {
		plan.Status.ResolvedNodes = res.SnapshotNodes
		plan.Status.NodeSnapshotTaken = true
	}

	return s.client.Status().Patch(
		ctx,
		plan,
		client.MergeFrom(original),
	)
}
