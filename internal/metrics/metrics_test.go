package metrics

import (
	"testing"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testPlan builds a plan with three nodes covering the full range of states:
// one draining+blocked, one ready, one drifted.
//
// Derived expectations:
//
//	ready=1, draining=1, blocked=1, drifted=1
//	progress = (50+100+0)/3/100 = 0.5
func testPlan(name string) *v1alpha1.NodeMaintenancePlan {
	return &v1alpha1.NodeMaintenancePlan{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1alpha1.NodeMaintenancePlanStatus{
			NodeCount: 3,
			Phase:     "Draining",
			Nodes: []v1alpha1.NodeStatus{
				{Name: "worker-1", TotalPods: 4, DrainProgress: 50, BlockedPods: 1, EvictedTotal: 2},
				{Name: "worker-2", TotalPods: 0, DrainProgress: 100, ReadyForMaintenance: true, EvictedTotal: 5},
				{Name: "worker-3", TotalPods: 0, DrainProgress: 0, Drifted: true},
			},
		},
	}
}

func assertGauge(t *testing.T, want float64, got float64, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}

func TestRecordPlan_PlanLevelCounters(t *testing.T) {
	plan := testPlan(t.Name())
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan)

	assertGauge(t, 3, testutil.ToFloat64(planManagedNodes.WithLabelValues(plan.Name)), "managedNodes")
	assertGauge(t, 1, testutil.ToFloat64(planReadyNodes.WithLabelValues(plan.Name)), "readyNodes")
	assertGauge(t, 1, testutil.ToFloat64(planDrainingNodes.WithLabelValues(plan.Name)), "drainingNodes")
	assertGauge(t, 1, testutil.ToFloat64(planBlockedNodes.WithLabelValues(plan.Name)), "blockedNodes")
	assertGauge(t, 1, testutil.ToFloat64(planDriftedNodes.WithLabelValues(plan.Name)), "driftedNodes")
	assertGauge(t, 0.5, testutil.ToFloat64(planDrainProgress.WithLabelValues(plan.Name)), "drainProgress")
}

func TestRecordPlan_Phase(t *testing.T) {
	plan := testPlan(t.Name())
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan)

	for _, p := range allPhases {
		want := 0.0
		if p == "Draining" {
			want = 1.0
		}
		assertGauge(t, want, testutil.ToFloat64(planPhase.WithLabelValues(plan.Name, p)), "phase="+p)
	}
}

func TestRecordPlan_PhaseTransition(t *testing.T) {
	plan := testPlan(t.Name())
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan)
	assertGauge(t, 1, testutil.ToFloat64(planPhase.WithLabelValues(plan.Name, "Draining")), "initial phase")

	plan.Status.Phase = "Ready"
	RecordPlan(plan)

	assertGauge(t, 1, testutil.ToFloat64(planPhase.WithLabelValues(plan.Name, "Ready")), "new phase")
	assertGauge(t, 0, testutil.ToFloat64(planPhase.WithLabelValues(plan.Name, "Draining")), "old phase cleared")
}

func TestRecordPlan_PerNode(t *testing.T) {
	plan := testPlan(t.Name())
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan)

	assertGauge(t, 0.5, testutil.ToFloat64(nodeDrainProgress.WithLabelValues(plan.Name, "worker-1")), "worker-1 progress")
	assertGauge(t, 2, testutil.ToFloat64(nodeEvictedTotal.WithLabelValues(plan.Name, "worker-1")), "worker-1 evicted")
	assertGauge(t, 1, testutil.ToFloat64(nodeBlockedPods.WithLabelValues(plan.Name, "worker-1")), "worker-1 blocked")

	assertGauge(t, 1, testutil.ToFloat64(nodeDrainProgress.WithLabelValues(plan.Name, "worker-2")), "worker-2 progress")
	assertGauge(t, 5, testutil.ToFloat64(nodeEvictedTotal.WithLabelValues(plan.Name, "worker-2")), "worker-2 evicted")
	assertGauge(t, 0, testutil.ToFloat64(nodeBlockedPods.WithLabelValues(plan.Name, "worker-2")), "worker-2 blocked")
}

func TestRecordPlan_NoNodes(t *testing.T) {
	plan := &v1alpha1.NodeMaintenancePlan{
		ObjectMeta: metav1.ObjectMeta{Name: t.Name()},
		Status: v1alpha1.NodeMaintenancePlanStatus{
			NodeCount: 0,
			Phase:     "Pending",
		},
	}
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan) // must not panic

	assertGauge(t, 0, testutil.ToFloat64(planManagedNodes.WithLabelValues(plan.Name)), "managedNodes")
	assertGauge(t, 0, testutil.ToFloat64(planDrainProgress.WithLabelValues(plan.Name)), "drainProgress")
	assertGauge(t, 1, testutil.ToFloat64(planPhase.WithLabelValues(plan.Name, "Pending")), "phase=Pending")
}

func TestDeletePlan_ClearsMetrics(t *testing.T) {
	plan := testPlan(t.Name())
	// Cleanup re-runs DeletePlan to remove any zero-valued series left by the
	// assertions below (testutil.ToFloat64 re-creates a gauge with value 0 when
	// the series has been deleted).
	t.Cleanup(func() { DeletePlan(plan) })

	RecordPlan(plan)
	assertGauge(t, 3, testutil.ToFloat64(planManagedNodes.WithLabelValues(plan.Name)), "before delete")

	DeletePlan(plan)

	assertGauge(t, 0, testutil.ToFloat64(planManagedNodes.WithLabelValues(plan.Name)), "plan-level cleared")
	assertGauge(t, 0, testutil.ToFloat64(nodeDrainProgress.WithLabelValues(plan.Name, "worker-1")), "node series cleared")
}
