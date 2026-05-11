// Reconcile contains the core logic for reconciling a NodeMaintenancePlan resource.

package maintenance

import (
	"context"
	"fmt"


	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

func (s *MaintenanceService) ReconcileOwnership(ctx context.Context,plan *v1alpha1.NodeMaintenancePlan,res *OwnershipResolution) error {

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
		if err := s.AdoptNode(ctx, node, plan); err != nil {
			return fmt.Errorf(
				"adopting node %q: %w",
				node.Name,
				err,
			)
		}
	}

	return nil
}


func (s *MaintenanceService) ReconcileCordon(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled

	// Handle desired owned nodes.
	for _, node := range res.Stable {
		if cordonEnabled {
			// Cooperative drift: manually uncordoned node.
			if !node.Spec.Unschedulable {
				s.log.Info(
					"manual uncordon detected, releasing ownership",
					"node", node.Name,
					"plan", plan.Name,
				)
				s.recorder.Eventf(
					plan,
					"Warning",
					"DriftDetected",
					"node %q manually uncordoned, ownership released",
					node.Name,
				)
				if err := s.ReleaseNode(ctx, node, plan); err != nil {
					return fmt.Errorf(
						"releasing node %q: %w",
						node.Name,
						err,
					)
				}

				continue
			}

		} else {
			// Cordon disabled: release operational control.
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
	}

	// Handle newly adopted nodes.
	if cordonEnabled {
		for _, node := range res.ToAdopt {
			// Only cordon if needed.
			if !node.Spec.Unschedulable {
				if err := s.CordonNode(ctx, node); err != nil {
					return fmt.Errorf(
						"cordoning node %q: %w",
						node.Name,
						err,
					)
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