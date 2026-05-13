package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

// ReconcileDrift releases ownership of any stable node that has drifted from the desired state.
// The annotation is removed so the operator stops managing the node, but the node itself is not
// mutated further — the user's manual change is preserved. The node stays in the plan spec and
// will be marked as drifted in status; the operator will not re-adopt it until the user removes
// it from the spec.
func (s *MaintenanceService) ReconcileDrift(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	for _, node := range res.Stable {

		drifted, reason := DetectNodeDrift(node, plan)

		if !drifted {
			continue
		}

		s.log.Info("node drifted, releasing ownership", "node", node.Name, "reason", reason)

		s.recorder.Eventf(
			plan,
			"Warning",
			"DriftDetected",
			"node %q drifted (%s): releasing ownership, will not re-adopt until removed from spec",
			node.Name,
			reason,
		)

		if err := s.ReleaseNode(ctx, node, plan); err != nil {
			return fmt.Errorf("releasing drifted node %q: %w", node.Name, err)
		}
	}

	return nil
}
