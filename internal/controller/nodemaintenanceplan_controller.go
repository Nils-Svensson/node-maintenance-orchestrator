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

package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	corev1 "k8s.io/api/core/v1"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NodeMaintenancePlanReconciler reconciles a NodeMaintenancePlan object
type NodeMaintenancePlanReconciler struct {
	Client        client.Client
	Scheme        *runtime.Scheme
	Recorder      record.EventRecorder
	ManagerConfig *rest.Config
}

const finalizerName = "nodemaintenanceplan.finalizers.nmoo.io"

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

	start := time.Now()

	defer func() {
		log.Info(
			"Finished reconciliation",
			"duration", time.Since(start))
	}()

	log.Info("Reconciling")

	plan := &v1alpha1.NodeMaintenancePlan{}

	service := maintenance.NewMaintenanceService(r.Client, log, r.Recorder)

	err := r.Client.Get(ctx, req.NamespacedName, plan)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ctrl.Result{}, nil
		}

		log.Error(err, "Failed to get resource")
		return ctrl.Result{}, err
	}

	// Handle deletion with finalizer
	if !plan.DeletionTimestamp.IsZero() {

		log.Info("Handling deletion")

		if err := r.handleDeletion(ctx, log, plan); err != nil {
			log.Error(err, "Failed to handle deletion")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Add finalizer if not present. Do not return early — Update refreshes plan
	// in-place so it is safe to continue reconciling in the same pass.
	if err := r.ensureFinalizer(ctx, plan); err != nil {
		log.Error(err, "Failed to ensure finalizer")
		return ctrl.Result{}, err
	}

	res, err := service.ComputeOwnershipResolution(ctx, plan)
	if err != nil {
		log.Error(err, "Failed to compute ownership resolution")
		return ctrl.Result{}, err
	}

	if err := service.ReconcileOwnership(ctx, plan, res); err != nil {
		log.Error(err, "Failed to reconcile ownership")
		return ctrl.Result{}, err
	}

	// UpdateStatus runs before ReconcileDrift so that drift is written to status
	// while the managed-by annotation is still present on stable nodes. ReconcileDrift
	// removes the annotation, so any status update after that point cannot detect drift.
	if err := service.UpdateStatus(ctx, plan, res); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	if err := service.ReconcileDrift(ctx, plan, res); err != nil {
		log.Error(err, "Failed to reconcile drift")
		return ctrl.Result{}, err
	}

	if err := service.ReconcileCordon(ctx, plan, res); err != nil {
		log.Error(err, "Failed to reconcile cordon state")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

}

// SetupWithManager sets up the controller with the Manager.
// TODO: add predicates here to filter events and reduce unnecessary reconciles, GenerationChangedPredicate to
// only trigger on spec changes, and watcher for nodes to trigger reconciles
// on relevant node events (e.g. become unschedulable, new nodes added to cluster, manual triggers outside of plan etc.)
func (r *NodeMaintenancePlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NodeMaintenancePlan{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToPlans), builder.WithPredicates(nodeMaintenancePredicates(logf.Log.WithName("node-predicate")))).
		Named("nodemaintenanceplan").
		Complete(r)
}

func (r *NodeMaintenancePlanReconciler) ensureFinalizer(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) error {

	if controllerutil.ContainsFinalizer(plan, finalizerName) {
		return nil
	}

	controllerutil.AddFinalizer(plan, finalizerName)

	return r.Client.Update(ctx, plan)
}

func (r *NodeMaintenancePlanReconciler) handleDeletion(ctx context.Context, log logr.Logger, plan *v1alpha1.NodeMaintenancePlan) error {
	if !controllerutil.ContainsFinalizer(plan, finalizerName) {
		return nil
	}
	svc := maintenance.NewMaintenanceService(r.Client, log, r.Recorder)
	if err := svc.CleanUp(ctx, plan); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(plan, finalizerName)
	return r.Client.Update(ctx, plan)
}

func (r *NodeMaintenancePlanReconciler) nodeToPlans(ctx context.Context, obj client.Object) []reconcile.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}

	log := logf.FromContext(ctx)

	var planList v1alpha1.NodeMaintenancePlanList
	if err := r.Client.List(ctx, &planList); err != nil {
		log.Error(err, "nodeToPlans: failed to list NodeMaintenancePlans")
		return nil
	}

	var requests []reconcile.Request
	for _, plan := range planList.Items {
		if nodeRelevantToPlan(node, &plan) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: plan.Name},
			})
		}
	}
	return requests
}