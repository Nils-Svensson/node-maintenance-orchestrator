package maintenance_test

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

var (
	epoch  = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past   = metav1.NewTime(epoch.Add(-1 * time.Hour))
	future = metav1.NewTime(epoch.Add(1 * time.Hour))
)

func TestCheckSchedule(t *testing.T) {
	tests := []struct {
		name          string
		startAt       *metav1.Time
		alreadyActed  bool
		wantShouldAct bool
		wantRequeue   bool
	}{
		{
			name:          "no startAt, not yet acted",
			startAt:       nil,
			alreadyActed:  false,
			wantShouldAct: true,
		},
		{
			name:          "no startAt, already acted",
			startAt:       nil,
			alreadyActed:  true,
			wantShouldAct: false,
		},
		{
			name:          "startAt in the past",
			startAt:       &past,
			alreadyActed:  false,
			wantShouldAct: true,
		},
		{
			name:          "startAt in the future",
			startAt:       &future,
			alreadyActed:  false,
			wantShouldAct: false,
			wantRequeue:   true,
		},
		{
			name:          "startAt in the future but already acted",
			startAt:       &future,
			alreadyActed:  true,
			wantShouldAct: false,
			wantRequeue:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maintenance.CheckSchedule(tt.startAt, tt.alreadyActed, epoch)
			if result.ShouldAct != tt.wantShouldAct {
				t.Errorf("ShouldAct = %v, want %v", result.ShouldAct, tt.wantShouldAct)
			}
			if tt.wantRequeue && result.RequeueAfter == 0 {
				t.Error("expected non-zero RequeueAfter, got 0")
			}
			if !tt.wantRequeue && result.RequeueAfter != 0 {
				t.Errorf("expected zero RequeueAfter, got %v", result.RequeueAfter)
			}
		})
	}
}

func TestCheckSchedule_RequeueIncludesBuffer(t *testing.T) {
	result := maintenance.CheckSchedule(&future, false, epoch)
	expected := future.Sub(epoch) + 100*time.Millisecond
	if result.RequeueAfter != expected {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, expected)
	}
}

func TestComputeSchedule_CordonDisabled(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	result := svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Error("Cordon.ShouldAct should be true when cordon is disabled (cleanup path)")
	}
	if result.Cordon.RequeueAfter != 0 {
		t.Errorf("unexpected Cordon.RequeueAfter %v when cordon disabled", result.Cordon.RequeueAfter)
	}
}

func TestComputeSchedule_CordonEnabledNoStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true}

	result := svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Error("Cordon.ShouldAct should be true when cordon enabled with no startAt")
	}
}

func TestComputeSchedule_CordonEnabledFutureStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &future}

	result := svc.ComputeSchedule(plan)
	if result.Cordon.ShouldAct {
		t.Error("Cordon.ShouldAct should be false when startAt is in the future")
	}
	if result.Cordon.RequeueAfter == 0 {
		t.Error("expected non-zero Cordon.RequeueAfter for future startAt")
	}
}

func TestComputeSchedule_CordonEnabledPastStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &past}

	result := svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Error("Cordon.ShouldAct should be true when startAt is in the past")
	}
}

func TestComputeSchedule_DrainDisabled(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	result := svc.ComputeSchedule(plan)
	if result.Drain.ShouldAct {
		t.Error("Drain.ShouldAct should be false when drain is disabled")
	}
}

func TestComputeSchedule_DrainEnabledNoStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: true}

	result := svc.ComputeSchedule(plan)
	if !result.Drain.ShouldAct {
		t.Error("Drain.ShouldAct should be true when drain enabled with no startAt")
	}
}

func TestComputeSchedule_DrainEnabledFutureStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: true, StartAt: &future}

	result := svc.ComputeSchedule(plan)
	if result.Drain.ShouldAct {
		t.Error("Drain.ShouldAct should be false when drain startAt is in the future")
	}
	if result.Drain.RequeueAfter == 0 {
		t.Error("expected non-zero Drain.RequeueAfter for future drain startAt")
	}
}

func TestComputeSchedule_CordonNowDrainLater(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: true, StartAt: &future}

	result := svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Error("Cordon.ShouldAct should be true (no startAt)")
	}
	if result.Drain.ShouldAct {
		t.Error("Drain.ShouldAct should be false (future startAt)")
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter from pending drain schedule")
	}
}

func TestComputeSchedule_RequeueIsMinNonZero(t *testing.T) {
	cordonFuture := metav1.NewTime(epoch.Add(30 * time.Minute))
	drainFuture := metav1.NewTime(epoch.Add(90 * time.Minute))

	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &cordonFuture}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: true, StartAt: &drainFuture}

	result := svc.ComputeSchedule(plan)
	if result.RequeueAfter != result.Cordon.RequeueAfter {
		t.Errorf("RequeueAfter should be min of cordon/drain, got %v want %v",
			result.RequeueAfter, result.Cordon.RequeueAfter)
	}
}

// TestComputeSchedule_StartAtPushedOut verifies that if startAt is updated to a later
// time after it has already passed, the operator correctly defers again rather than
// continuing to act.
func TestComputeSchedule_StartAtPushedOut(t *testing.T) {
	T := metav1.NewTime(epoch.Add(1 * time.Hour))
	Tx := metav1.NewTime(epoch.Add(2 * time.Hour))

	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &T}

	// Before T: not yet active.
	result := svc.ComputeSchedule(plan)
	if result.Cordon.ShouldAct {
		t.Fatal("expected Cordon.ShouldAct=false before startAt")
	}

	// Advance clock past T: now active.
	fakeClock.SetTime(epoch.Add(90 * time.Minute))
	result = svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Fatal("expected Cordon.ShouldAct=true after startAt has passed")
	}

	// startAt is pushed out to T+x (still in the future): must defer again.
	plan.Spec.Cordon.StartAt = &Tx
	result = svc.ComputeSchedule(plan)
	if result.Cordon.ShouldAct {
		t.Error("expected Cordon.ShouldAct=false after startAt was pushed to a future time")
	}
	if result.Cordon.RequeueAfter == 0 {
		t.Error("expected non-zero Cordon.RequeueAfter after startAt pushed out")
	}

	// Advance clock past T+x: active again.
	fakeClock.SetTime(epoch.Add(3 * time.Hour))
	result = svc.ComputeSchedule(plan)
	if !result.Cordon.ShouldAct {
		t.Error("expected Cordon.ShouldAct=true after updated startAt has passed")
	}
}
