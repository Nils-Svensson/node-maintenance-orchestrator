package maintenance

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

const scheduleBuffer = 100 * time.Millisecond

type ScheduleResult struct {
	// ShouldAct is true if the scheduled time has arrived.
	ShouldAct bool
	// RequeueAfter is set when ShouldAct is false, indicating how long to wait.
	RequeueAfter time.Duration
}

// CheckSchedule determines whether a scheduled action should proceed given the current time.
// startAt nil means "act immediately". alreadyActed should be true if the action has
// already been performed (e.g. cordon already applied), in which case the schedule is inert.
func CheckSchedule(startAt *metav1.Time, alreadyActed bool, now time.Time) ScheduleResult {
	if alreadyActed {
		return ScheduleResult{ShouldAct: false}
	}
	if startAt == nil || !now.Before(startAt.Time) {
		return ScheduleResult{ShouldAct: true}
	}
	return ScheduleResult{
		ShouldAct:    false,
		RequeueAfter: startAt.Time.Sub(now) + scheduleBuffer,
	}
}

// ComputeSchedule returns the schedule result for the plan's cordon operation,
// using the service's injected clock.
//
// When cordon is disabled, ShouldAct is always true so that ReconcileCordon can
// clean up any operator-applied cordons regardless of scheduling.
//
// alreadyActed is always false for cordon because cordon is an ongoing state to
// maintain, not a one-shot action. It is meaningful for drain (future).
//
// TODO: extend to include drain once drain scheduling is implemented.
func (s *MaintenanceService) ComputeSchedule(plan *v1alpha1.NodeMaintenancePlan) ScheduleResult {
	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled
	if !cordonEnabled {
		return ScheduleResult{ShouldAct: true}
	}
	var startAt *metav1.Time
	if plan.Spec.Cordon != nil {
		startAt = plan.Spec.Cordon.StartAt
	}
	return CheckSchedule(startAt, false, s.clock.Now())
}
