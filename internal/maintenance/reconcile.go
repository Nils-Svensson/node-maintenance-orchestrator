// Reconcile contains the core logic for reconciling a NodeMaintenancePlan resource.

package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// ReconcileOwnership adopts and releases nodes according to the ownership resolution.
// cordonNow should be true when the cordon schedule is currently active; it gates
// whether nodes are cordoned at the moment of adoption.
func (s *MaintenanceService) ReconcileOwnership(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution, cordonNow bool) error {

	for _, node := range res.Conflicting {
		s.log.Info(
			"node already managed by another plan",
			"node", node.Name,
			"plan", plan.Name,
		)
		s.recorder.Eventf(
			plan,
			node,
			"Warning",
			"OwnershipConflict",
			"SkipNode",
			"node %q already managed by another plan",
			node.Name,
		)
	}

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled

	for _, node := range res.ToAdopt {
		if drifted, _ := GetNodeDriftState(plan, node.Name); drifted {
			s.log.V(1).Info("skipping re-adoption of drifted node", "node", node.Name)
			continue
		}
		if nodeCompletedMaintenance(plan, node.Name) {
			s.log.V(1).Info("skipping re-adoption of node that completed maintenance", "node", node.Name)
			continue
		}
		if err := s.AdoptNode(ctx, node, plan, cordonEnabled && cordonNow); err != nil {
			return fmt.Errorf("adopting node %q: %w", node.Name, err)
		}
	}

	return nil
}

func (s *MaintenanceService) ReconcileCordon(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled

	for _, node := range res.Stable {
		// Skip nodes that ReconcileDrift already released in this same pass.
		if node.Annotations[ManagedByAnnotation] != plan.Name {
			continue
		}

		if cordonEnabled {
			changed, err := s.CordonNode(ctx, node)
			if err != nil {
				return fmt.Errorf("cordoning node %q: %w", node.Name, err)
			}
			if changed {
				s.recorder.Eventf(plan, node, corev1.EventTypeNormal, "NodeCordoned", "CordonNode", "node %q cordoned", node.Name)
			}
		} else {
			// A node cordoned by an external actor (no operator annotation) is left
			// alone; ReconcileDrift has already logged the event.
			if node.Spec.Unschedulable && node.Annotations[CordonedAnnotation] != "true" {
				continue
			}
			changed, err := s.UncordonNode(ctx, node)
			if err != nil {
				return fmt.Errorf("uncordoning node %q: %w", node.Name, err)
			}
			if changed {
				s.recorder.Eventf(plan, node, corev1.EventTypeNormal, "NodeUncordoned", "UncordonNode", "node %q uncordoned", node.Name)
			}
		}
	}

	// Nodes removed from desired state.
	for _, node := range res.ToRelease {
		if _, err := s.UncordonNode(ctx, node); err != nil {
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

		s.log.Info("node removed from plan spec and released", "node", node.Name)
		s.recorder.Eventf(plan, node, corev1.EventTypeNormal, "NodeReleased", "ReleaseNode",
			"node %q removed from plan spec, uncordoned and released", node.Name)
	}

	return nil
}
