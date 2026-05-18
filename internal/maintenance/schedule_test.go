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
	expected := future.Time.Sub(epoch) + 100*time.Millisecond
	if result.RequeueAfter != expected {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, expected)
	}
}

func TestComputeSchedule_CordonDisabled(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	// cordon not set at all
	result := svc.ComputeSchedule(plan)
	if !result.ShouldAct {
		t.Error("ShouldAct should be true when cordon is disabled (cleanup path)")
	}
	if result.RequeueAfter != 0 {
		t.Errorf("unexpected RequeueAfter %v when cordon disabled", result.RequeueAfter)
	}
}

func TestComputeSchedule_CordonEnabledNoStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true}

	result := svc.ComputeSchedule(plan)
	if !result.ShouldAct {
		t.Error("ShouldAct should be true when cordon enabled with no startAt")
	}
}

func TestComputeSchedule_CordonEnabledFutureStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &future}

	result := svc.ComputeSchedule(plan)
	if result.ShouldAct {
		t.Error("ShouldAct should be false when startAt is in the future")
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter for future startAt")
	}
}

func TestComputeSchedule_CordonEnabledPastStartAt(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &past}

	result := svc.ComputeSchedule(plan)
	if !result.ShouldAct {
		t.Error("ShouldAct should be true when startAt is in the past")
	}
}

// TestComputeSchedule_StartAtPushedOut verifies that if startAt is updated to a later
// time after it has already passed, the operator correctly defers again rather than
// continuing to act.
func TestComputeSchedule_StartAtPushedOut(t *testing.T) {
	// Clock starts at epoch. T is 1h ahead, T+x is 2h ahead.
	T := metav1.NewTime(epoch.Add(1 * time.Hour))
	Tx := metav1.NewTime(epoch.Add(2 * time.Hour))

	fakeClock := clocktesting.NewFakeClock(epoch)
	svc := maintenance.NewMaintenanceService(nil, logr.Discard(), nil, fakeClock)

	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true, StartAt: &T}

	// Before T: not yet active.
	result := svc.ComputeSchedule(plan)
	if result.ShouldAct {
		t.Fatal("expected ShouldAct=false before startAt")
	}

	// Advance clock past T: now active.
	fakeClock.SetTime(epoch.Add(90 * time.Minute))
	result = svc.ComputeSchedule(plan)
	if !result.ShouldAct {
		t.Fatal("expected ShouldAct=true after startAt has passed")
	}

	// startAt is pushed out to T+x (still in the future): must defer again.
	plan.Spec.Cordon.StartAt = &Tx
	result = svc.ComputeSchedule(plan)
	if result.ShouldAct {
		t.Error("expected ShouldAct=false after startAt was pushed to a future time")
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter after startAt pushed out")
	}

	// Advance clock past T+x: active again.
	fakeClock.SetTime(epoch.Add(3 * time.Hour))
	result = svc.ComputeSchedule(plan)
	if !result.ShouldAct {
		t.Error("expected ShouldAct=true after updated startAt has passed")
	}
}
