package maintenance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

// notReadyThreshold mirrors the constant in node_health.go so tests stay in sync.
const notReadyThreshold = 300 * time.Second

// newDrainService builds a MaintenanceService with a controlled clock and a fake
// client that has status-subresource semantics enabled for NodeMaintenancePlan.
func newDrainService(clk clock.Clock, objects ...client.Object) (*maintenance.MaintenanceService, *events.FakeRecorder) {
	fc := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		WithStatusSubresource(&v1alpha1.NodeMaintenancePlan{}).
		Build()
	rec := events.NewFakeRecorder(10)
	return maintenance.NewMaintenanceService(fc, logr.Discard(), rec, clk), rec
}

// makeNotReadyCordoned builds a cordoned NotReady node managed by the given plan.
func makeNotReadyCordoned(name, planName string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				maintenance.ManagedByAnnotation: planName,
				maintenance.CordonedAnnotation:  "true",
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}
}

// makeDrainPlan builds a minimal plan with cordon+drain enabled and a pre-populated
// NodeStatus entry so setNodeIssues can locate the node.
func makeDrainPlan(planName string, nodeName string, notReadySince *metav1.Time) *v1alpha1.NodeMaintenancePlan {
	return &v1alpha1.NodeMaintenancePlan{
		ObjectMeta: metav1.ObjectMeta{Name: planName},
		Spec: v1alpha1.NodeMaintenancePlanSpec{
			Nodes:  []string{nodeName},
			Cordon: &v1alpha1.CordonSpec{Enabled: true},
			Drain:  &v1alpha1.DrainSpec{Enabled: true},
		},
		Status: v1alpha1.NodeMaintenancePlanStatus{
			Nodes: []v1alpha1.NodeStatus{{
				Name:          nodeName,
				NotReadySince: notReadySince,
			}},
		},
	}
}

func hasDrainBlocked(plan *v1alpha1.NodeMaintenancePlan) bool {
	for _, c := range plan.Status.Conditions {
		if c.Type == v1alpha1.ConditionDrainBlocked {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

func hasNodeNotReadyIssue(plan *v1alpha1.NodeMaintenancePlan, nodeName string) bool {
	for _, ns := range plan.Status.Nodes {
		if ns.Name != nodeName {
			continue
		}
		for _, issue := range ns.Issues {
			if issue.Type == "NodeNotReady" {
				return true
			}
		}
	}
	return false
}

// TestReconcileDrain_NodeNotReady_FirstDetection covers the initial reconcile where
// NotReadySince is not yet set in status (UpdateStatus hasn't run yet). No yield event
// should fire and DrainBlocked must be True.
func TestReconcileDrain_NodeNotReady_FirstDetection(t *testing.T) {
	const nodeName, planName = "node-1", "plan-1"
	node := makeNotReadyCordoned(nodeName, planName)
	plan := makeDrainPlan(planName, nodeName, nil) // NotReadySince not set yet

	svc, rec := newDrainService(clock.RealClock{}, node, plan)

	_, err := svc.ReconcileDrain(context.Background(), plan, &maintenance.OwnershipResolution{
		Stable: []*corev1.Node{node},
	})
	if err != nil {
		t.Fatalf("ReconcileDrain: %v", err)
	}

	if !hasDrainBlocked(plan) {
		t.Error("expected DrainBlocked=True for NotReady node")
	}
	if !hasNodeNotReadyIssue(plan, nodeName) {
		t.Error("expected NodeNotReady issue in node status")
	}
	// No yield event on first detection.
	select {
	case event := <-rec.Events:
		t.Errorf("unexpected event on first detection: %q", event)
	default:
	}
}

// TestReconcileDrain_NodeNotReady_NoYieldBelowThreshold verifies that no yield event
// fires when NotReadySince is set but the elapsed time is below the 300s threshold.
func TestReconcileDrain_NodeNotReady_NoYieldBelowThreshold(t *testing.T) {
	now := time.Now()
	const nodeName, planName = "node-1", "plan-1"
	node := makeNotReadyCordoned(nodeName, planName)
	plan := makeDrainPlan(planName, nodeName, &metav1.Time{Time: now.Add(-10 * time.Second)})

	svc, rec := newDrainService(clocktesting.NewFakeClock(now), node, plan)

	_, err := svc.ReconcileDrain(context.Background(), plan, &maintenance.OwnershipResolution{
		Stable: []*corev1.Node{node},
	})
	if err != nil {
		t.Fatalf("ReconcileDrain: %v", err)
	}

	if !hasDrainBlocked(plan) {
		t.Error("expected DrainBlocked=True for NotReady node")
	}
	if !hasNodeNotReadyIssue(plan, nodeName) {
		t.Error("expected NodeNotReady issue in node status")
	}
	// No yield event when below threshold.
	select {
	case event := <-rec.Events:
		t.Errorf("unexpected yield event below threshold: %q", event)
	default:
	}
}

// TestReconcileDrain_NodeNotReady_YieldsAboveThreshold verifies that a NodeNotReady
// warning event containing "yielding" is fired once NotReadySince exceeds 300s.
func TestReconcileDrain_NodeNotReady_YieldsAboveThreshold(t *testing.T) {
	now := time.Now()
	const nodeName, planName = "node-1", "plan-1"
	node := makeNotReadyCordoned(nodeName, planName)
	// NotReadySince is one second past the threshold.
	plan := makeDrainPlan(planName, nodeName, &metav1.Time{Time: now.Add(-(notReadyThreshold + time.Second))})

	svc, rec := newDrainService(clocktesting.NewFakeClock(now), node, plan)

	_, err := svc.ReconcileDrain(context.Background(), plan, &maintenance.OwnershipResolution{
		Stable: []*corev1.Node{node},
	})
	if err != nil {
		t.Fatalf("ReconcileDrain: %v", err)
	}

	if !hasDrainBlocked(plan) {
		t.Error("expected DrainBlocked=True for yielded NotReady node")
	}

	// A single warning event should contain "NodeNotReady" and "yielding".
	select {
	case event := <-rec.Events:
		if !strings.Contains(event, "NodeNotReady") || !strings.Contains(event, "yielding") {
			t.Errorf("yield event %q does not contain expected strings", event)
		}
	default:
		t.Error("expected NodeNotReady yield event but channel was empty")
	}
}
