package maintenance

import (
	"context"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *MaintenanceService) CleanUp(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) error {
	// Placeholder for cleanup logic, such as removing old maintenance records, resetting states, etc.
	s.log.Info("Performing cleanup tasks for node maintenance operations")
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
		log.V(1).Info(
			"node not managed by this plan, skipping release",
		)
		return nil
	}

	original := node.DeepCopy()

	delete(node.Annotations, ManagedByAnnotation)
	delete(node.Annotations, ReasonAnnotation)

	log.Info("releasing node ownership")

	return s.client.Patch(
		ctx,
		node,
		client.MergeFrom(original),
	)
}