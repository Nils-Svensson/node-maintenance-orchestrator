package maintenance

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

func TestComputePhase(t *testing.T) {
	cond := func(condType string) func(*v1alpha1.NodeMaintenancePlan) {
		return func(p *v1alpha1.NodeMaintenancePlan) {
			setCondition(p, condType, metav1.ConditionTrue, "Reason", "msg")
		}
	}

	allReady := func(p *v1alpha1.NodeMaintenancePlan) {
		p.Status.AllNodesReadyForMaintenance = true
	}

	tests := []struct {
		name      string
		setup     []func(*v1alpha1.NodeMaintenancePlan)
		wantPhase string
	}{
		// Individual phase triggers
		{"no conditions → Pending", nil, "Pending"},
		{"NodesSelected → Adopted", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionNodesSelected)}, "Adopted"},
		{"Scheduled → Scheduled", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionScheduled)}, "Scheduled"},
		{"Cordoned → Cordoned", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionCordoned)}, "Cordoned"},
		{"DrainInProgress → Draining", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainInProgress)}, "Draining"},
		{"DrainBlocked → Blocked", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainBlocked)}, "Blocked"},
		{"AllNodesReady → Ready", []func(*v1alpha1.NodeMaintenancePlan){allReady}, "Ready"},
		{"Conflict → Conflict", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionConflict)}, "Conflict"},
		{"DrainTimedOut → TimedOut", []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainTimedOut)}, "TimedOut"},

		// Precedence: higher-priority phase wins when multiple are set
		{
			name:      "TimedOut beats Conflict",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainTimedOut), cond(v1alpha1.ConditionConflict)},
			wantPhase: "TimedOut",
		},
		{
			name:      "Conflict beats Ready",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionConflict), allReady},
			wantPhase: "Conflict",
		},
		{
			name:      "Ready beats Blocked",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){allReady, cond(v1alpha1.ConditionDrainBlocked)},
			wantPhase: "Ready",
		},
		{
			name:      "Blocked beats Draining",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainBlocked), cond(v1alpha1.ConditionDrainInProgress)},
			wantPhase: "Blocked",
		},
		{
			name:      "Draining beats Cordoned",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionDrainInProgress), cond(v1alpha1.ConditionCordoned)},
			wantPhase: "Draining",
		},
		// NodeNotReady alone (drain=false scenario) does not change the phase —
		// the condition surfaces the issue without overriding the plan-wide phase.
		{
			name:      "NodeNotReady alone does not change phase",
			setup:     []func(*v1alpha1.NodeMaintenancePlan){cond(v1alpha1.ConditionNodesSelected), cond(v1alpha1.ConditionNodeNotReady)},
			wantPhase: "Adopted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &v1alpha1.NodeMaintenancePlan{}
			for _, fn := range tt.setup {
				fn(plan)
			}
			got := computePhase(plan)
			if got != tt.wantPhase {
				t.Errorf("computePhase = %q, want %q", got, tt.wantPhase)
			}
		})
	}
}
