package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *MaintenanceService) CleanUp(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) error {
	s.log.Info("cleaning up plan")

	// TODO: cancel any in-flight drain operations when drain is implemented.

	owned, err := s.ResolveOwnedNodes(ctx, plan.Name)
	if err != nil {
		return fmt.Errorf("resolving owned nodes: %w", err)
	}

	for _, node := range owned {
		if _, err := s.UncordonNode(ctx, node); err != nil {
			return fmt.Errorf("uncordoning node %q during cleanup: %w", node.Name, err)
		}
		if err := s.ReleaseNode(ctx, node, plan); err != nil {
			return fmt.Errorf("releasing node %q during cleanup: %w", node.Name, err)
		}
		s.recorder.Eventf(
			plan,
			"Normal",
			"NodeReleased",
			"node %q uncordoned and released as part of plan deletion",
			node.Name,
		)
	}

	return nil
}

// ReleaseNode removes maintenance-related annotations from the node, effectively releasing it from maintenance management.
// This is typically called after uncordoning a node to clean up any metadata that indicates it was under maintenance.
func (s *MaintenanceService) ReleaseNode(ctx context.Context, node *corev1.Node, plan *v1alpha1.NodeMaintenancePlan) error {

	log := s.log.WithValues("node", node.Name)

	if node.Annotations == nil {
		return nil
	}

	if node.Annotations[ManagedByAnnotation] != plan.Name {
		log.V(1).Info("node not managed by this plan, skipping release")
		return nil
	}

	original := node.DeepCopy()

	delete(node.Annotations, ManagedByAnnotation)
	delete(node.Annotations, ReasonAnnotation)
	delete(node.Annotations, CordonedAnnotation)

	log.Info("releasing node ownership")

	return s.client.Patch(ctx, node, client.MergeFrom(original))
}
