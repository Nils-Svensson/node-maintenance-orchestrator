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

	"k8s.io/api/core/v1"
	
)



// CordonNode marks the given node as unschedulable, effectively cordoning it. It also adds annotations to indicate that the node is managed by the maintenance orchestrator and optionally includes a reason for cordoning.
// The operation is idempotent, so if the node is already cordoned, it will simply log that information and return without making changes.
func (s *MaintenanceService) CordonNode(ctx context.Context, node *v1.Node, reason string) error {

	log := s.log.WithValues("node", node.Name)

	if node.Spec.Unschedulable {
		log.V(1).Info("node already cordoned")
		return nil
	}

	node.Spec.Unschedulable = true

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Annotations["maintenance.nmoo.io/managed"] = "true"

	if reason != "" {
		node.Annotations["maintenance.nmoo.io/reason"] = reason
	}

	log.Info("cordoning node")

	return s.client.Update(ctx, node)
}
// UncordonNode marks the given node as schedulable, effectively uncordoning it. It also removes any annotations related to maintenance management and reason. 
// The operation is idempotent, so if the node is already uncordoned, it will simply log that information and return without making changes.
func (s *MaintenanceService) UncordonNode(ctx context.Context, node *v1.Node, reason string) error {

	log := s.log.WithValues("node", node.Name)

	if !node.Spec.Unschedulable {
		log.V(1).Info("node already uncordoned")
		return nil
	}

	node.Spec.Unschedulable = false

	if node.Annotations != nil {
		delete(node.Annotations, "maintenance.nmoo.io/managed")
		delete(node.Annotations, "maintenance.nmoo.io/reason")
	}

	log.Info("uncordoning node")

	return s.client.Update(ctx, node)
	
}