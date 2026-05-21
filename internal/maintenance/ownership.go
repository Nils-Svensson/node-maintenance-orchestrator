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

	// SnapshotNodes is non-nil only on the first reconcile of a NodeSelector-based
	// plan. UpdateStatus uses it to persist the snapshot to status before the
	// selector is re-evaluated on subsequent passes.
	SnapshotNodes []string
}

// ComputeOwnershipResolution computes the ownership resolution for a given NodeMaintenancePlan
// by comparing the desired nodes (resolved from the plan) with the currently managed nodes (annotated in the cluster).
// It returns an OwnershipResolution that categorizes nodes into those to adopt, release, stable, or conflicting. Any errors encountered during resolution are returned for handling by the caller.
//
// For NodeSelector-based plans, the node set is frozen on the first reconcile
// (stored in status.ResolvedNodes). Subsequent reconciles use the snapshot so
// that nodes added to the cluster after plan creation are never auto-adopted.
func (s *MaintenanceService) ComputeOwnershipResolution(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) (*OwnershipResolution, error) {
	var result *NodeResolutionResult
	var snapshotNodes []string // non-nil signals UpdateStatus to persist the snapshot

	switch {
	case plan.Spec.NodeSelector != nil && plan.Status.NodeSnapshotTaken:
		// Subsequent reconcile: use the frozen snapshot instead of re-evaluating the selector.
		var err error
		result, err = s.resolveSnapshotNodes(ctx, plan.Status.ResolvedNodes)
		if err != nil {
			return nil, fmt.Errorf("resolving snapshot nodes: %w", err)
		}

	case plan.Spec.NodeSelector != nil && !plan.Status.NodeSnapshotTaken:
		// First reconcile: resolve the selector and freeze the result.
		var err error
		result, err = s.resolveSelectorNodes(ctx, plan)
		if err != nil {
			return nil, fmt.Errorf("resolving selector nodes: %w", err)
		}
		snapshotNodes = make([]string, 0, len(result.Nodes))
		for i := range result.Nodes {
			snapshotNodes = append(snapshotNodes, result.Nodes[i].Name)
		}

	default:
		var err error
		result, err = s.ResolveNodes(ctx, plan)
		if err != nil {
			return nil, fmt.Errorf("resolving desired nodes: %w", err)
		}
	}

	desired := make(map[string]*corev1.Node)
	if result != nil {
		for i := range result.Nodes {
			node := &result.Nodes[i]
			desired[node.Name] = node
		}
	}

	managed, err := s.ResolveOwnedNodes(ctx, plan.Name)
	if err != nil {
		return nil, fmt.Errorf("resolving owned nodes: %w", err)
	}

	res := ComputeOwnership(desired, managed, plan.Name)
	res.SnapshotNodes = snapshotNodes
	return res, nil
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

		switch owner {
		case "":
			res.ToAdopt = append(res.ToAdopt, node)
		case planName:
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

	// TODO: Add label maintenance.nmoo.io/in-maintenance = "true, either here or once cordon is active,
	// which can be used by other operators (e.g. autoscaler) to avoid scheduling new pods on the node.
	// This should maybe be set regardless of cordon behavior, since even if cordon is disabled,
	// the plan still considers the node in maintenance and would want to avoid new scheduling.
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
		log.Info("adopting node ownership and cordoning")
	} else {
		log.Info("adopting node ownership")
	}

	if err := s.client.Patch(ctx, node, client.MergeFrom(original)); err != nil {
		return err
	}

	if cordon {
		s.recorder.Eventf(plan, corev1.EventTypeNormal, "NodeCordoned", "node %q adopted and cordoned", node.Name)
	} else {
		s.recorder.Eventf(plan, corev1.EventTypeNormal, "NodeAdopted", "node %q adopted", node.Name)
	}
	return nil
}
