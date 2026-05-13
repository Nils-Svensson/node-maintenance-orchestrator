// cordon.go contains logic for safely cordoning and uncordoning nodes.
//
// It encapsulates all mutations to the Node resource related to scheduling,
// specifically setting and removing the Unschedulable flag.
//
// This package ensures idempotent operations and isolates node mutation logic
// from higher-level orchestration in the controller.

package maintenance

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CordonNode marks the given node as unschedulable, effectively cordoning it.
// The operation is idempotent, so if the node is already cordoned, it will simply log that information and return without making changes.
func (s *MaintenanceService) CordonNode(ctx context.Context, node *corev1.Node) error {

	log := s.log.WithValues("node", node.Name)

	if node.Spec.Unschedulable {
		log.V(1).Info("node already cordoned")
		return nil
	}

	original := node.DeepCopy()

	node.Spec.Unschedulable = true

	log.Info("cordoning node")

	return s.client.Patch(
		ctx,
		node,
		client.MergeFrom(original),
	)
}

// UncordonNode marks the given node as schedulable, effectively uncordoning it.
// The operation is idempotent, so if the node is already uncordoned, it will simply log that information and return without making changes.
func (s *MaintenanceService) UncordonNode(ctx context.Context, node *corev1.Node) error {

	log := s.log.WithValues("node", node.Name)

	original := node.DeepCopy()

	if !node.Spec.Unschedulable {
		log.V(1).Info("node already uncordoned")
		return nil
	}

	node.Spec.Unschedulable = false

	log.Info("uncordoning node")

	return s.client.Patch(ctx, node, client.MergeFrom(original))
}
