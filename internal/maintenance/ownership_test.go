package maintenance_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

// TestComputeOwnership_ConflictingNode verifies that a desired node already
// annotated by a different plan ends up in Conflicting, not ToAdopt.
func TestComputeOwnership_ConflictingNode(t *testing.T) {
	const (
		ownerPlan    = "plan-a"
		conflictPlan = "plan-b"
		nodeName     = "shared-node"
	)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nodeName,
			Annotations: map[string]string{maintenance.ManagedByAnnotation: ownerPlan},
		},
	}

	res := maintenance.ComputeOwnership(
		map[string]*corev1.Node{nodeName: node},
		map[string]*corev1.Node{},
		conflictPlan,
	)

	if len(res.Conflicting) != 1 || res.Conflicting[0].Name != nodeName {
		t.Errorf("expected 1 conflicting node %q, got %v", nodeName, res.Conflicting)
	}
	if len(res.ToAdopt) != 0 {
		t.Errorf("expected no nodes to adopt, got %v", res.ToAdopt)
	}
}

// TestReconcileOwnership_ConflictingNode verifies the reconciler-level fallback
// when the webhook is bypassed: a node owned by plan A must not be re-annotated
// by plan B, and plan B must emit an OwnershipConflict warning event.
func TestReconcileOwnership_ConflictingNode(t *testing.T) {
	const (
		planAName = "plan-a"
		planBName = "plan-b"
		nodeName  = "shared-node"
	)

	node := makeNode(nodeName, false, map[string]string{
		maintenance.ManagedByAnnotation: planAName,
	})
	planB := makePlan(planBName, false)
	svc, recorder, fakeClient := newService(node, planB)

	res := &maintenance.OwnershipResolution{
		Conflicting: []*corev1.Node{node},
		All:         []*corev1.Node{node},
	}
	if err := svc.ReconcileOwnership(context.Background(), planB, res, false); err != nil {
		t.Fatalf("ReconcileOwnership: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: nodeName}, &got); err != nil {
		t.Fatalf("Get node: %v", err)
	}
	if got.Annotations[maintenance.ManagedByAnnotation] != planAName {
		t.Errorf("node ownership should remain %q, got %q", planAName, got.Annotations[maintenance.ManagedByAnnotation])
	}
	requireEvent(t, recorder, "OwnershipConflict")
}
