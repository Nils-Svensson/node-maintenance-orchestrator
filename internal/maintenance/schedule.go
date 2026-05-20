package maintenance

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

// Maybe tweak this buffer after testing in real scenarios.
// It is intended to account for small discrepancies in scheduling and controller execution time,
// so it should be large enough to prevent unnecessary requeues but small enough
// to not cause noticeable delays when the schedule is actually due.
const scheduleBuffer = 100 * time.Millisecond

type ScheduleResult struct {
	// ShouldAct is true if the scheduled time has arrived.
	ShouldAct bool
	// RequeueAfter is set when ShouldAct is false, indicating how long to wait.
	RequeueAfter time.Duration
}

// PlanSchedule holds independent schedule results for each maintenance phase.
type PlanSchedule struct {
	Cordon ScheduleResult
	Drain  ScheduleResult
	// RequeueAfter is the minimum non-zero duration across all phases.
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
		RequeueAfter: startAt.Sub(now) + scheduleBuffer,
	}
}

// ComputeSchedule returns per-phase schedule results for the plan.
//
// Cordon: when disabled, ShouldAct=true so ReconcileCordon can clean up any
// operator-applied cordons. When enabled, gated by cordon.startAt.
//
// Drain: when disabled, ShouldAct=false. When enabled, gated by drain.startAt.
// alreadyActed is false for both phases — each phase manages its own idempotency.
func (s *MaintenanceService) ComputeSchedule(plan *v1alpha1.NodeMaintenancePlan) PlanSchedule {
	now := s.clock.Now()

	cordonEnabled := plan.Spec.Cordon != nil && plan.Spec.Cordon.Enabled
	var cordon ScheduleResult
	if !cordonEnabled {
		cordon = ScheduleResult{ShouldAct: true}
	} else {
		cordon = CheckSchedule(plan.Spec.Cordon.StartAt, false, now)
	}

	drainEnabled := plan.Spec.Drain != nil && plan.Spec.Drain.Enabled
	var drain ScheduleResult
	if drainEnabled {
		drain = CheckSchedule(plan.Spec.Drain.StartAt, false, now)
	}

	// Both non-zero: pick the sooner requeue. One zero: max picks the non-zero value.
	requeue := max(cordon.RequeueAfter, drain.RequeueAfter)
	if cordon.RequeueAfter > 0 && drain.RequeueAfter > 0 {
		requeue = min(cordon.RequeueAfter, drain.RequeueAfter)
	}

	return PlanSchedule{
		Cordon:       cordon,
		Drain:        drain,
		RequeueAfter: requeue,
	}
}
