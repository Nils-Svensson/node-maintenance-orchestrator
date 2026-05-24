package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// ReconcileDrift releases ownership of any stable node that has drifted from the desired state.
// The annotation is removed so the operator stops managing the node, but the node itself is not
// mutated further — the user's manual change is preserved. The node stays in the plan spec and
// will be marked as drifted in status; the operator will not re-adopt it until the user removes
// it from the spec.
// TODO: I need to figure out how to handle the case where nodeselector is used,
// since removing from plan spec won't be a viable option for the user to re-adopt.
// Maybe in that case we should re-adopt immediately and just log the drift without releasing ownership, since the user has no way to "fix" the drift?
func (s *MaintenanceService) ReconcileDrift(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	for _, node := range res.Stable {

		drifted, reason := DetectNodeDrift(node, plan)

		if !drifted {
			continue
		}

		switch reason {
		case DriftReasonMaintenanceComplete:
			s.log.Info("node uncordoned after maintenance, releasing ownership", "node", node.Name)
			s.recorder.Eventf(
				plan,
				corev1.EventTypeNormal,
				"MaintenanceComplete",
				"node %q returned to service after maintenance: ownership released",
				node.Name,
			)
			if err := s.ReleaseNode(ctx, node, plan); err != nil {
				return fmt.Errorf("releasing maintenance-complete node %q: %w", node.Name, err)
			}

		case DriftReasonManualUncordon:
			s.log.Info("node drifted, releasing ownership", "node", node.Name, "reason", reason)
			s.recorder.Eventf(
				plan,
				corev1.EventTypeWarning,
				"DriftDetected",
				"node %q drifted (%s): ownership released. Remove and re-add the node to the plan spec to resume management.",
				node.Name,
				reason,
			)
			if err := s.ReleaseNode(ctx, node, plan); err != nil {
				return fmt.Errorf("releasing drifted node %q: %w", node.Name, err)
			}

		case DriftReasonExternalCordon:
			s.log.Info("node externally cordoned while cordon is disabled, skipping", "node", node.Name)
			s.recorder.Eventf(
				plan,
				corev1.EventTypeWarning,
				"DriftDetected",
				"node %q drifted (%s): externally cordoned while cordon is disabled, operator will not interfere",
				node.Name,
				reason,
			)
		}
	}

	return nil
}
