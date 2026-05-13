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

	statuses := make([]v1alpha1.NodeStatus, 0, len(res.All))

	for _, node := range res.All {

		drifted, reason := DetectNodeDrift(node, plan)

		// If not currently detectable (annotation already removed by a prior
		// ReconcileDrift), carry forward the drift state from the previous status
		// so the node remains marked drifted until removed from the spec.
		if !drifted {
			drifted, reason = GetNodeDriftState(plan, node.Name)
		}

		statuses = append(statuses, v1alpha1.NodeStatus{
			Name:        node.Name,
			Cordoned:    node.Spec.Unschedulable,
			Drifted:     drifted,
			DriftReason: reason,
		})
	}

	original := plan.DeepCopy()

	plan.Status.Nodes = statuses

	return s.client.Status().Patch(
		ctx,
		plan,
		client.MergeFrom(original),
	)
}
