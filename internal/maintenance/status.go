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

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

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
