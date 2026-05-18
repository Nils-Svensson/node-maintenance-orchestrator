package maintenance_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

func TestDetectNodeDrift(t *testing.T) {
	const planName = "test-plan"

	node := func(unschedulable bool, annotations map[string]string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", Annotations: annotations},
			Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		}
	}

	plan := func(cordonEnabled bool) *v1alpha1.NodeMaintenancePlan {
		p := &v1alpha1.NodeMaintenancePlan{ObjectMeta: metav1.ObjectMeta{Name: planName}}
		if cordonEnabled {
			p.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true}
		}
		return p
	}

	managed := map[string]string{maintenance.ManagedByAnnotation: planName}
	managedAndCordoned := map[string]string{
		maintenance.ManagedByAnnotation: planName,
		maintenance.CordonedAnnotation:  "true",
	}

	tests := []struct {
		name        string
		node        *corev1.Node
		plan        *v1alpha1.NodeMaintenancePlan
		wantDrifted bool
		wantReason  string
	}{
		{
			name: "nil node",
			node: nil,
			plan: plan(false),
		},
		{
			name: "nil annotations",
			node: node(true, nil),
			plan: plan(false),
		},
		{
			name: "node managed by a different plan",
			node: node(true, map[string]string{maintenance.ManagedByAnnotation: "other-plan"}),
			plan: plan(false),
		},
		// cordon disabled cases
		{
			name: "cordon disabled / node schedulable",
			node: node(false, managed),
			plan: plan(false),
		},
		{
			name:        "cordon disabled / externally cordoned",
			node:        node(true, managed),
			plan:        plan(false),
			wantDrifted: true,
			wantReason:  maintenance.DriftReasonExternalCordon,
		},
		{
			name: "cordon disabled / operator-cordoned annotation present (operator cordoned previously)",
			node: node(true, managedAndCordoned),
			plan: plan(false),
		},
		// cordon enabled cases
		{
			name: "cordon enabled / operator cordoned and unschedulable",
			node: node(true, managedAndCordoned),
			plan: plan(true),
		},
		{
			name:        "cordon enabled / operator cordoned / manually uncordoned",
			node:        node(false, managedAndCordoned),
			plan:        plan(true),
			wantDrifted: true,
			wantReason:  maintenance.DriftReasonManualUncordon,
		},
		{
			name: "cordon enabled / not yet cordoned by operator / node schedulable",
			node: node(false, managed),
			plan: plan(true),
		},
		{
			// Pre-schedule window: plan wants cordon but startAt is in the future.
			// Not detected as drift until scheduling logic is implemented.
			name: "cordon enabled / not yet cordoned by operator / externally cordoned",
			node: node(true, managed),
			plan: plan(true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drifted, reason := maintenance.DetectNodeDrift(tt.node, tt.plan)
			if drifted != tt.wantDrifted {
				t.Errorf("drifted = %v, want %v", drifted, tt.wantDrifted)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
