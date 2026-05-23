package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// drainConfig holds resolved drain options with defaults applied.
// MaxParallel is intentionally excluded — it is a plan-level concurrency
// concern handled by drain_reconcile.go, not a per-node execution concern.
type drainConfig struct {
	IgnoreDaemonSets                 bool
	DeleteEmptyDirData               bool
	Force                            bool
	PodTerminationGracePeriodSeconds *int64
	RespectPodDisruptionBudgets      bool
}

// drainOutcome carries per-node counters back to ReconcileDrain so it can
// compute requeue intervals and update NodeStatus without re-listing pods.
type drainOutcome struct {
	Evicted     int // eviction requests sent in this reconcile pass
	Total       int // evictable + config-blocked pods at classification time
	Evictable   int // pods that were evictable at classification time
	Terminating int // pods already evicted, waiting for physical removal
}

// drainBlockedError is returned when pods on the node prevent drain from proceeding.
type drainBlockedError struct {
	node       string
	pods       []corev1.Pod
	pdbBlocked bool
}

func (e *drainBlockedError) Error() string {
	if e.pdbBlocked {
		return fmt.Sprintf("node %s: drain blocked by PDB on %d pod(s)", e.node, len(e.pods))
	}
	return fmt.Sprintf("node %s: drain blocked by %d unevictable pod(s)", e.node, len(e.pods))
}

func (s *MaintenanceService) drainNode(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, node *corev1.Node) (drainOutcome, error) {
	cfg := getDrainConfig(plan)
	if cfg == nil {
		return drainOutcome{}, fmt.Errorf("drain not enabled for plan %s/%s", plan.Namespace, plan.Name)
	}

	result, err := s.filterPodsForDrain(ctx, node, cfg)
	if err != nil {
		return drainOutcome{}, err
	}

	outcome := drainOutcome{
		Total:       len(result.Evictable) + len(result.Blocked),
		Evictable:   len(result.Evictable),
		Terminating: len(result.Terminating),
	}

	if len(result.Blocked) > 0 {
		return outcome, &drainBlockedError{node: node.Name, pods: result.Blocked}
	}

	for i := range result.Evictable {
		pod := &result.Evictable[i]
		if err := s.evictPod(ctx, pod, cfg.PodTerminationGracePeriodSeconds); err != nil {
			if apierrors.IsTooManyRequests(err) {
				if !cfg.RespectPodDisruptionBudgets {
					// PDB checks bypassed — delete the pod directly.
					if delErr := s.client.Delete(ctx, pod); delErr != nil && !apierrors.IsNotFound(delErr) {
						return outcome, fmt.Errorf("force-deleting PDB-blocked pod %s/%s: %w", pod.Namespace, pod.Name, delErr)
					}
					outcome.Evicted++
					continue
				}
				return outcome, &drainBlockedError{node: node.Name, pods: []corev1.Pod{*pod}, pdbBlocked: true}
			}
			if apierrors.IsNotFound(err) {
				// Pod was already deleted between classification and eviction.
				continue
			}
			return outcome, fmt.Errorf("evicting pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		outcome.Evicted++
	}
	return outcome, nil
}

// evictPod sends an eviction request for a pod. If gracePeriodSeconds is non-nil
// it overrides the pod's terminationGracePeriodSeconds.
func (s *MaintenanceService) evictPod(ctx context.Context, pod *corev1.Pod, gracePeriodSeconds *int64) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}
	if gracePeriodSeconds != nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{
			GracePeriodSeconds: gracePeriodSeconds,
		}
	}
	return s.client.SubResource("eviction").Create(ctx, pod, eviction)
}

// getDrainConfig resolves drain options from the plan, applying CRD defaults
// when the options block is absent. The result matches standard kubectl drain
// behaviour when no options are configured.
func getDrainConfig(plan *v1alpha1.NodeMaintenancePlan) *drainConfig {
	if plan.Spec.Drain == nil || !plan.Spec.Drain.Enabled {
		return nil
	}
	if plan.Spec.Drain.Options == nil {
		return &drainConfig{
			IgnoreDaemonSets:            true, // matches CRD default
			RespectPodDisruptionBudgets: true,
		}
	}
	opts := plan.Spec.Drain.Options
	return &drainConfig{
		IgnoreDaemonSets:                 opts.IgnoreDaemonSets,
		DeleteEmptyDirData:               opts.DeleteEmptyDirData,
		Force:                            opts.Force,
		PodTerminationGracePeriodSeconds: opts.PodTerminationGracePeriodSeconds,
		RespectPodDisruptionBudgets:      opts.RespectPodDisruptionBudgets,
	}
}
