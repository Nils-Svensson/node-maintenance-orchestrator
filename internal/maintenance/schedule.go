package maintenance


import (
	"time"
	
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)



func (s *MaintenanceService) Scheduler(plan *v1alpha1.NodeMaintenancePlan, time metav1.Time) (requeueAfter time.Duration, err error) {


	
	return requeueAfter, nil
}