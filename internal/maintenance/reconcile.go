// Reconcile contains the core logic for reconciling a NodeMaintenancePlan resource.

package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

func (s *MaintenanceService) ReconcileOwnership(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	for _, node := range res.Conflicting {
		s.log.Info(
			"node already managed by another plan",
			"node", node.Name,
			"plan", plan.Name,
		)
		s.recorder.Eventf(
			plan,
			"Warning",
			"OwnershipConflict",
			"node %q already managed by another plan",
			node.Name,
		)
	}

	for _, node := range res.ToAdopt {
		if drifted, _ := GetNodeDriftState(plan, node.Name); drifted {
			s.log.V(1).Info("skipping re-adoption of drifted node", "node", node.Name)
			continue
		}
		if err := s.AdoptNode(ctx, node, plan); err != nil {
			return fmt.Errorf("adopting node %q: %w", node.Name, err)
		}
	}

	return nil
}

func (s *MaintenanceService) ReconcileCordon(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled

	// Stable nodes: cordon enabled means nothing to do (drift handled by ReconcileDrift).
	// If cordon was disabled, release operational control.
	if !cordonEnabled {
		for _, node := range res.Stable {
			if err := s.UncordonNode(ctx, node); err != nil {
				return fmt.Errorf("uncordoning node %q: %w", node.Name, err)
			}
			if err := s.ReleaseNode(ctx, node, plan); err != nil {
				return fmt.Errorf("releasing node %q: %w", node.Name, err)
			}
		}
	}

	// Handle newly adopted nodes.
	if cordonEnabled {
		for _, node := range res.ToAdopt {
			if drifted, _ := GetNodeDriftState(plan, node.Name); drifted {
				continue
			}
			if !node.Spec.Unschedulable {
				if err := s.CordonNode(ctx, node); err != nil {
					return fmt.Errorf("cordoning node %q: %w", node.Name, err)
				}
			}
		}
	}

	// Nodes removed from desired state.
	for _, node := range res.ToRelease {
		if err := s.UncordonNode(ctx, node); err != nil {
			return fmt.Errorf(
				"uncordoning node %q: %w",
				node.Name,
				err,
			)
		}

		if err := s.ReleaseNode(ctx, node, plan); err != nil {
			return fmt.Errorf(
				"releasing node %q: %w",
				node.Name,
				err,
			)
		}
	}

	return nil
}
