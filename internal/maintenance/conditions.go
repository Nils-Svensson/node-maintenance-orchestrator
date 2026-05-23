package maintenance

import (
	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setCondition upserts a condition on the plan using the standard apimeta helper,
// which deduplicates by Type and updates LastTransitionTime only when Status changes.
func setCondition(plan *v1alpha1.NodeMaintenancePlan, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&plan.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: plan.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func isConditionTrue(plan *v1alpha1.NodeMaintenancePlan, condType string) bool {
	c := apimeta.FindStatusCondition(plan.Status.Conditions, condType)
	return c != nil && c.Status == metav1.ConditionTrue
}
