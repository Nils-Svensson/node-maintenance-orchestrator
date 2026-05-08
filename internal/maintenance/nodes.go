// Package maintenance contains core logic for node maintenance operations.
//
// nodes.go is responsible for resolving the set of nodes targeted by a
// NodeMaintenancePlan. It supports both explicit node lists and label-based
// selectors, and ensures a consistent and deduplicated set of nodes.
//
// This layer abstracts node discovery away from the controller so that
// reconciliation logic can operate on a resolved set of nodes without
// concern for how they were selected.

package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeResolutionResult encapsulates the outcome of resolving nodes for a NodeMaintenancePlan. It includes the list of resolved nodes and any issues encountered during resolution, such as missing nodes or invalid selectors.
type NodeResolutionResult struct {
	Nodes []v1.Node
	Issues []v1alpha1.NodeIssue
}

// ResolveNodes determines the set of nodes targeted by the given NodeMaintenancePlan. It supports both explicit node lists and label-based selectors, returning any issues encountered during resolution.
func (s *MaintenanceService) ResolveNodes(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) (*NodeResolutionResult, error) {
	switch {
	case len(plan.Spec.Nodes) > 0:
		return s.resolveExplicitNodes(ctx, plan)

	case plan.Spec.NodeSelector != nil:
		return s.resolveSelectorNodes(ctx, plan)

	default:
		return nil, nil
	}

}

// resolveExplicitNodes retrieves nodes explicitly listed in the plan. It returns any issues encountered during resolution, such as nodes that do not exist.
func (s *MaintenanceService) resolveExplicitNodes(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) (*NodeResolutionResult, error) {

	result := &NodeResolutionResult{}

	for _, nodeName := range plan.Spec.Nodes {

		var node v1.Node

		err := s.client.Get(ctx, types.NamespacedName{Name: nodeName}, &node)

		if err != nil {

			if apierrors.IsNotFound(err) {
				result.Issues = append(result.Issues,
					v1alpha1.NodeIssue{
						Type:    "NodeNotFound",
						Message: fmt.Sprintf("node %q does not exist", nodeName),
					},
				)

				continue
			}

			return nil, err
		}

		result.Nodes = append(result.Nodes, node)
	}

	return result, nil
}

// resolveSelectorNodes retrieves nodes matching the provided label selector in the plan. It returns any issues encountered during resolution, such as an invalid selector or no matching nodes.
func (s *MaintenanceService) resolveSelectorNodes(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) (*NodeResolutionResult, error) {

	result := &NodeResolutionResult{}

	selector, err := metav1.LabelSelectorAsSelector(plan.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid node selector: %w", err)
	}

	var nodeList v1.NodeList

	err = s.client.List(
		ctx,
		&nodeList,
		client.MatchingLabelsSelector{
			Selector: selector,
		},
	)

	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	if len(nodeList.Items) == 0 {
		result.Issues = append(result.Issues,
			v1alpha1.NodeIssue{
				Type:    "NoMatchingNodes",
				Message: "no nodes match the provided selector",
			},
		)
	}

	result.Nodes = append(result.Nodes, nodeList.Items...)

	return result, nil
}