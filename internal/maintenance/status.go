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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

// recomputePlanSummaries updates Phase, ReadySummary, DrainProgress,
// DrainingNodeCount, and BlockedNodeCount from the current per-node status.
// Call it whenever plan.Status.Nodes changes.
func recomputePlanSummaries(plan *v1alpha1.NodeMaintenancePlan) {
	var ready, draining, blocked, totalProgress int32
	anyDrifted := false
	total := int32(len(plan.Status.Nodes))

	for _, ns := range plan.Status.Nodes {
		totalProgress += ns.DrainProgress
		if ns.ReadyForMaintenance {
			ready++
		} else if ns.TotalPods > 0 && ns.BlockedPods < ns.TotalPods {
			draining++
		}
		if ns.BlockedPods > 0 && ns.BlockedPods == ns.TotalPods {
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
		return v1alpha1.PhaseTimedOut
	case isConditionTrue(plan, v1alpha1.ConditionConflict):
		return v1alpha1.PhaseConflict
	case plan.Status.AllNodesReadyForMaintenance:
		return v1alpha1.PhaseReady
	case isConditionTrue(plan, v1alpha1.ConditionDrainBlocked):
		return v1alpha1.PhaseBlocked
	case isConditionTrue(plan, v1alpha1.ConditionDrainInProgress):
		return v1alpha1.PhaseDraining
	case isConditionTrue(plan, v1alpha1.ConditionCordoned):
		return v1alpha1.PhaseCordoned
	case isConditionTrue(plan, v1alpha1.ConditionScheduled):
		return v1alpha1.PhaseScheduled
	case isConditionTrue(plan, v1alpha1.ConditionNodesSelected):
		return v1alpha1.PhaseAdopted
	default:
		return v1alpha1.PhasePending
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

	// Detect nodes that were mid-drain in the previous reconcile but are now absent
	// from res.All and res.ToRelease. Nodes in ToRelease were intentionally removed
	// from the plan spec; nodes absent from both disappeared from the cluster.
	resAllNames := make(map[string]struct{}, len(res.All))
	for _, n := range res.All {
		resAllNames[n.Name] = struct{}{}
	}
	resToReleaseNames := make(map[string]struct{}, len(res.ToRelease))
	for _, n := range res.ToRelease {
		resToReleaseNames[n.Name] = struct{}{}
	}
	for _, ns := range existing {
		if _, inAll := resAllNames[ns.Name]; inAll {
			continue
		}
		if _, inRelease := resToReleaseNames[ns.Name]; inRelease {
			continue
		}
		if ns.InitialPodCount > 0 && ns.DrainProgress < 100 {
			s.log.Info("node disappeared from cluster mid-drain", "node", ns.Name)
			s.recorder.Eventf(plan, corev1.EventTypeWarning, "NodeDisappeared",
				"node %q disappeared from the cluster while drain was in progress", ns.Name)
		}
	}

	now := metav1.NewTime(s.clock.Now())
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

		// Track NotReadySince: set when first observed NotReady, clear on recovery.
		// Resetting on every recovery ensures short flips don't accumulate toward
		// the threshold prematurely.
		if !isNodeReady(node) {
			if ns.NotReadySince == nil {
				ns.NotReadySince = &now
			}
		} else {
			ns.NotReadySince = nil
		}

		statuses = append(statuses, ns)
	}

	original := plan.DeepCopy()

	plan.Status.Nodes = statuses
	plan.Status.NodeCount = int32(len(statuses))
	plan.Status.ObservedGeneration = plan.Generation

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
		plan.Spec.Cordon.StartAt.After(s.clock.Now())
	if isScheduled {
		setCondition(plan, v1alpha1.ConditionScheduled, metav1.ConditionTrue,
			"ScheduledInFuture", fmt.Sprintf("cordon scheduled for %s",
				plan.Spec.Cordon.StartAt.UTC().Format(time.RFC3339)))
	} else {
		setCondition(plan, v1alpha1.ConditionScheduled, metav1.ConditionFalse,
			"NotScheduled", "No pending scheduled activation")
	}

	// DriftDetected — at least one managed node has diverged from desired state.
	var driftedNames []string
	for _, ns := range statuses {
		if ns.Drifted {
			driftedNames = append(driftedNames, ns.Name)
		}
	}
	if len(driftedNames) > 0 {
		setCondition(plan, v1alpha1.ConditionDriftDetected, metav1.ConditionTrue,
			"NodeDrifted", fmt.Sprintf("%d node(s) drifted: %s", len(driftedNames), strings.Join(driftedNames, ", ")))
	} else {
		setCondition(plan, v1alpha1.ConditionDriftDetected, metav1.ConditionFalse,
			"NoDrift", "No managed nodes have drifted")
	}

	// NodeNotReady — one or more managed nodes have been NotReady beyond the threshold.
	// When drain is enabled, ReconcileDrain owns the "yielding" event; here we only
	// emit a warning on the drain=false path so the operator is never silent about a
	// node that Kubernetes has been silently draining for >300s.
	drainEnabled := plan.Spec.Drain != nil && plan.Spec.Drain.Enabled
	var notReadyNames []string
	for _, ns := range statuses {
		if ns.NotReadySince != nil && s.clock.Since(ns.NotReadySince.Time) >= nodeNotReadyThreshold {
			notReadyNames = append(notReadyNames, ns.Name)
			if !drainEnabled {
				s.recorder.Eventf(plan, corev1.EventTypeWarning, "NodeNotReady",
					"node %q has been NotReady for >%ds; Kubernetes node lifecycle controller is managing pod eviction",
					ns.Name, int(nodeNotReadyThreshold.Seconds()))
			}
		}
	}
	if len(notReadyNames) > 0 {
		setCondition(plan, v1alpha1.ConditionNodeNotReady, metav1.ConditionTrue,
			"NodeNotReady", fmt.Sprintf("%d node(s) NotReady beyond threshold: %s",
				len(notReadyNames), strings.Join(notReadyNames, ", ")))
	} else {
		setCondition(plan, v1alpha1.ConditionNodeNotReady, metav1.ConditionFalse,
			"AllNodesReady", "All managed nodes are healthy")
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
