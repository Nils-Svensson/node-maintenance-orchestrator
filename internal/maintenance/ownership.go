package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ManagedByAnnotation = "maintenance.nmoo.io/managed-by"
	ReasonAnnotation    = "maintenance.nmoo.io/reason"
	// CordonedAnnotation is set on a node when the operator has cordoned it.
	// Drift detection requires this marker so it can distinguish "never cordoned
	// by the operator" from "operator cordoned it and someone manually undid that."
	CordonedAnnotation = "maintenance.nmoo.io/cordoned"
)

// OwnershipResolution is the result of diffing desired vs currently managed nodes.
type OwnershipResolution struct {
	// Nodes in desired set not yet annotated as owned by this plan.
	ToAdopt []*corev1.Node

	// Nodes currently owned by this plan but no longer in the desired set.
	ToRelease []*corev1.Node

	// Nodes already correctly owned and in desired state.
	Stable []*corev1.Node

	// Nodes annotated as desired by current plan but owned by another plan.
	// This should be empty if admission controller is working correctly, but if not, these nodes will be left alone and logged for manual intervention.
	Conflicting []*corev1.Node

	All []*corev1.Node
}

// ComputeOwnershipResolution computes the ownership resolution for a given NodeMaintenancePlan
// by comparing the desired nodes (resolved from the plan) with the currently managed nodes (annotated in the cluster).
// It returns an OwnershipResolution that categorizes nodes into those to adopt, release, stable, or conflicting. Any errors encountered during resolution are returned for handling by the caller.
func (s *MaintenanceService) ComputeOwnershipResolution(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) (*OwnershipResolution, error) {

	resolutionResult, err := s.ResolveNodes(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf(
			"resolving desired nodes: %w",
			err,
		)
	}

	desired := make(map[string]*corev1.Node)

	if resolutionResult != nil {
		for i := range resolutionResult.Nodes {

			node := &resolutionResult.Nodes[i]

			desired[node.Name] = node
		}
	}

	managed, err := s.ResolveOwnedNodes(ctx, plan.Name)
	if err != nil {
		return nil, fmt.Errorf(
			"resolving owned nodes: %w",
			err,
		)
	}

	return ComputeOwnership(
		desired,
		managed,
		plan.Name,
	), nil
}

// ResolveOwnedNodes returns all cluster nodes annotated as managed by planName.
func (s *MaintenanceService) ResolveOwnedNodes(ctx context.Context, planName string) (map[string]*corev1.Node, error) {
	var nodeList corev1.NodeList
	if err := s.client.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	owned := make(map[string]*corev1.Node, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		if node.Annotations[ManagedByAnnotation] == planName {
			owned[node.Name] = node
		}
	}
	return owned, nil
}

// ComputeOwnership diffs desired vs managed into an OwnershipResolution.
func ComputeOwnership(desired map[string]*corev1.Node, managed map[string]*corev1.Node, planName string) *OwnershipResolution {

	res := &OwnershipResolution{}

	for _, node := range desired {

		owner := ""

		if node.Annotations != nil {
			owner = node.Annotations[ManagedByAnnotation]
		}

		switch {
		case owner == "":
			res.ToAdopt = append(res.ToAdopt, node)
		case owner == planName:
			res.Stable = append(res.Stable, node)
		default:
			res.Conflicting = append(res.Conflicting, node)
		}

		res.All = append(res.All, node)
	}

	for name, node := range managed {
		if _, ok := desired[name]; !ok {
			res.ToRelease = append(res.ToRelease, node)
		}
	}
	return res
}

// AdoptNode claims ownership of a node by setting the managed-by annotation.
// If cordon is true, Unschedulable is set in the same patch so the node never
// passes through the intermediate state of being annotated but still schedulable,
// which would otherwise look like ManualUncordon drift to a concurrent reconcile.
func (s *MaintenanceService) AdoptNode(ctx context.Context, node *corev1.Node, plan *v1alpha1.NodeMaintenancePlan, cordon bool) error {

	log := s.log.WithValues("node", node.Name)

	original := node.DeepCopy()

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Annotations[ManagedByAnnotation] = plan.Name
	node.Annotations[ReasonAnnotation] = plan.Spec.Reason

	if cordon {
		if !node.Spec.Unschedulable {
			node.Spec.Unschedulable = true
		}
		node.Annotations[CordonedAnnotation] = "true"
	}

	log.Info("adopting node ownership")

	return s.client.Patch(ctx, node, client.MergeFrom(original))
}
