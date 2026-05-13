package controller

import (
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

// nodeMaintenancePredicates filters node events to those that may affect a NodeMaintenancePlan.
// It is intended to be used with a Watches() call on corev1.Node.
func nodeMaintenancePredicates(log logr.Logger) predicate.Predicate {
	return predicate.Funcs{
		// A new node may match a selector-based plan or satisfy a previously unresolved named node.
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},

		// A deleted node that was managed needs status cleanup.
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},

		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok := e.ObjectOld.(*corev1.Node)
			if !ok {
				log.Info("failed to cast old object to Node", "object", e.ObjectOld)
				return false
			}
			newNode, ok := e.ObjectNew.(*corev1.Node)
			if !ok {
				log.Info("failed to cast new object to Node", "object", e.ObjectNew)
				return false
			}
			return nodeRelevantChange(oldNode, newNode)
		},

		// No external events are relevant.
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func nodeRelevantChange(old, new *corev1.Node) bool {
	// Cordon/uncordon on any node (drift detection for managed nodes).
	if old.Spec.Unschedulable != new.Spec.Unschedulable {
		return true
	}

	// Label changes on any node can add or remove it from a selector-based plan.
	if !reflect.DeepEqual(old.Labels, new.Labels) {
		return true
	}

	// Annotation changes only matter if the node is (or was) managed by this operator.
	// Covers: manual annotation removal, reason changes, or someone adding our annotation.
	if isManagedByOperator(old) || isManagedByOperator(new) {
		if !reflect.DeepEqual(old.Annotations, new.Annotations) {
			return true
		}
	}

	// Node entering termination while under management.
	if old.DeletionTimestamp == nil && new.DeletionTimestamp != nil {
		return true
	}

	return false
}

func isManagedByOperator(node *corev1.Node) bool {
	if node.Annotations == nil {
		return false
	}
	_, ok := node.Annotations[maintenance.ManagedByAnnotation]
	return ok
}

// nodeRelevantToPlan returns true if the given node is relevant to the plan —
// either it is explicitly listed, matches the selector, or is currently annotated
// as managed by this plan (important for delete events where the node may no
// longer be in the spec but cleanup is still needed).
func nodeRelevantToPlan(node *corev1.Node, plan *v1alpha1.NodeMaintenancePlan) bool {
	if node.Annotations[maintenance.ManagedByAnnotation] == plan.Name {
		return true
	}

	for _, name := range plan.Spec.Nodes {
		if name == node.Name {
			return true
		}
	}

	if plan.Spec.NodeSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(plan.Spec.NodeSelector)
		if err != nil {
			return false
		}
		if selector.Matches(labels.Set(node.Labels)) {
			return true
		}
	}

	return false
}
