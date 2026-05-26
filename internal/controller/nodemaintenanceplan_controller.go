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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NodeMaintenancePlanReconciler reconciles a NodeMaintenancePlan object
type NodeMaintenancePlanReconciler struct {
	Client   client.Client
	Recorder record.EventRecorder
}

const finalizerName = v1alpha1.NodeMaintenancePlanFinalizer

// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans/status,verbs=get;patch
// +kubebuilder:rbac:groups=maintenance.nmoo.io,resources=nodemaintenanceplans/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=list

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

	service := maintenance.NewMaintenanceService(r.Client, log, r.Recorder, clock.RealClock{})

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

	schedule := service.ComputeSchedule(plan)

	if err := service.ReconcileOwnership(ctx, plan, res, schedule.Cordon.ShouldAct); err != nil {
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

	if err := service.ReconcilePreview(ctx, plan, res); err != nil {
		log.Error(err, "Failed to reconcile preview")
		return ctrl.Result{}, err
	}

	if err := service.ReconcileDrift(ctx, plan, res); err != nil {
		log.Error(err, "Failed to reconcile drift")
		return ctrl.Result{}, err
	}

	if schedule.Cordon.ShouldAct {
		if err := service.ReconcileCordon(ctx, plan, res); err != nil {
			log.Error(err, "Failed to reconcile cordon state")
			return ctrl.Result{}, err
		}
	}

	var drainRequeue time.Duration
	if schedule.Drain.ShouldAct {
		var err error
		drainRequeue, err = service.ReconcileDrain(ctx, plan, res)
		if err != nil {
			log.Error(err, "Failed to reconcile drain state")
			return ctrl.Result{}, err
		}
	}

	// drainRequeue takes priority when drain is active; schedule.RequeueAfter
	// is used when the schedule has not yet fired (drainRequeue will be zero).
	requeueAfter := schedule.RequeueAfter
	if drainRequeue != 0 {
		requeueAfter = drainRequeue
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeMaintenancePlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register a cache index on spec.nodeName so filterPodsForDrain can use
	// client.MatchingFields{"spec.nodeName": name} against the cache efficiently.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &corev1.Pod{}, "spec.nodeName",
		func(obj client.Object) []string {
			return []string{obj.(*corev1.Pod).Spec.NodeName}
		},
	); err != nil {
		return fmt.Errorf("indexing pods by node name: %w", err)
	}

	// Pass through spec changes (generation bump) and deletion (DeletionTimestamp set).
	// GenerationChangedPredicate alone would filter the DeletionTimestamp update since
	// metadata changes don't increment generation, causing plans to get stuck in Terminating.
	planPredicate := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return !obj.GetDeletionTimestamp().IsZero()
		}),
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NodeMaintenancePlan{}, builder.WithPredicates(planPredicate)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToPlans), builder.WithPredicates(nodeMaintenancePredicates(logf.Log.WithName("node-predicate")))).
		Named("nodemaintenanceplan").
		Complete(r)
}

// ensureFinalizer adds the finalizer to the plan if not already present, so that the controller can perform cleanup on deletion.
func (r *NodeMaintenancePlanReconciler) ensureFinalizer(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan) error {
	if controllerutil.ContainsFinalizer(plan, finalizerName) {
		return nil
	}
	original := plan.DeepCopy()
	controllerutil.AddFinalizer(plan, finalizerName)
	return r.Client.Patch(ctx, plan, client.MergeFrom(original))
}

// handleDeletion performs cleanup when a NodeMaintenancePlan is deleted, then removes the finalizer to allow deletion to complete.
func (r *NodeMaintenancePlanReconciler) handleDeletion(ctx context.Context, log logr.Logger, plan *v1alpha1.NodeMaintenancePlan) error {
	if !controllerutil.ContainsFinalizer(plan, finalizerName) {
		return nil
	}
	svc := maintenance.NewMaintenanceService(r.Client, log, r.Recorder, clock.RealClock{})
	if err := svc.CleanUp(ctx, plan); err != nil {
		return err
	}
	original := plan.DeepCopy()
	controllerutil.RemoveFinalizer(plan, finalizerName)
	if err := r.Client.Patch(ctx, plan, client.MergeFrom(original)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// nodeToPlans maps a Node event to the set of NodeMaintenancePlans that reference the node in their spec,
// so that they can be reconciled in response to node changes. This allows the controller to react to relevant
// node changes without needing to watch all nodes and filter in Reconcile, which would be inefficient and cause unnecessary load on the API server.
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
