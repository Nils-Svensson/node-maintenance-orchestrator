package maintenance

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

const nodeNotReadyThreshold = 300 * time.Second

// isNodeReady returns false when the node's Ready condition is False or Unknown,
// or when no Ready condition is present.
func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeNotReadyIssue returns a NodeIssue for a NotReady node. yielded is true when
// the node has been NotReady beyond nodeNotReadyThreshold and NMO has yielded
// drain control to the Kubernetes node lifecycle controller.
func nodeNotReadyIssue(yielded bool) v1alpha1.NodeIssue {
	msg := "node is NotReady; drain will resume when the node recovers"
	if yielded {
		msg = "node has been NotReady for >300s; yielding drain to Kubernetes node lifecycle controller — manual verification may be required before marking maintenance complete"
	}
	return v1alpha1.NodeIssue{Type: "NodeNotReady", Message: msg}
}

// nodeNotReadyError is returned as a synthetic drainNodeResult error when a node
// is NotReady during drain. It carries a copy of the NotReadySince timestamp so
// applyDrainResults can decide whether the threshold has been crossed.
type nodeNotReadyError struct {
	node          string
	notReadySince *metav1.Time // copy of NodeStatus.NotReadySince at the time of classification
}

func (e *nodeNotReadyError) Error() string {
	return "node " + e.node + " is NotReady"
}
