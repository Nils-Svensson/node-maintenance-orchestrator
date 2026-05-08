/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
Remember workflow:

Reconcile:
  1. Fetch plan
  2. Handle deletion
  3. Resolve nodes
  4. Compute preview
  5. Update status
  6. Execute (cordon/drain)
  7. Requeue if needed
*/

package controller

import (
	"context"
	
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	

	maintenancev1alpha1 "github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	
)

// NodeMaintenancePlanReconciler reconciles a NodeMaintenancePlan object
type NodeMaintenancePlanReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Recorder record.EventRecorder
	ManagerConfig *rest.Config
}

// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodeMaintenancePlan object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *NodeMaintenancePlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := logf.FromContext(ctx).WithValues("plan", req.NamespacedName)

	log.Info("Reconciling")

	plan := &maintenancev1alpha1.NodeMaintenancePlan{}

	err := r.Client.Get(ctx, req.NamespacedName, plan)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ctrl.Result{}, nil
		}

		log.Error(err, "Failed to get resource")
		return ctrl.Result{}, err
	}

	if !plan.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Handling deletion")

		if controllerutil.ContainsFinalizer(plan, "nodemaintenanceplan.finalizers.nmoo.io") {
			// TODO: cleanup logic here, e.g. uncordon nodes if plan is deleted while in progress, 
			// remove any associated resources etc.

			controllerutil.RemoveFinalizer(plan, "nodemaintenanceplan.finalizers.nmoo.io")
			if err := r.Client.Update(ctx, plan); err != nil {
				log.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	if plan.Spec.Cordon.Enabled {
		
	}

	



	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// TODO: add predicates here to filter events and reduce unnecessary reconciles, GenerationChangedPredicate to
// only trigger on spec changes, and watcher for nodes to trigger reconciles 
// on relevant node events (e.g. become unschedulable, new nodes added to cluster, manual triggers outside of plan etc.)
func (r *NodeMaintenancePlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.NodeMaintenancePlan{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Named("nodemaintenanceplan").
		Complete(r)
}
