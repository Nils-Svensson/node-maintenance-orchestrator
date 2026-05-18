package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// drainCheckInterval is how soon the reconciler re-checks after evictions are
	// fired, to detect when terminating pods have been removed.
	// TODO: make dynamic based on the minimum terminationGracePeriodSeconds across
	// the evicted pods to avoid unnecessary reconciles.
	drainCheckInterval = 5 * time.Second

	// drainBlockedRetry is the requeue interval when all nodes are blocked (PDB or
	// config). Longer than drainCheckInterval since the block won't self-clear quickly.
	drainBlockedRetry = 15 * time.Second
)

type drainNodeResult struct {
	NodeName string
	Outcome  drainOutcome
	Err      error
}

// ReconcileDrain drives drain for all cordoned nodes owned by the plan.
// It returns a suggested requeue duration: non-zero while drain is in progress
// so the reconciler re-checks without waiting for an external event.
func (s *MaintenanceService) ReconcileDrain(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) (time.Duration, error) {
	cfg := getDrainConfig(plan)
	if cfg == nil {
		return 0, nil
	}

	maxParallel := 1
	if plan.Spec.Drain.Options != nil && plan.Spec.Drain.Options.MaxParallel > 0 {
		maxParallel = int(plan.Spec.Drain.Options.MaxParallel)
	}

	// Only drain nodes that are already cordoned — cordon must precede drain.
	var nodesToDrain []*corev1.Node
	for _, node := range res.Stable {
		if node.Spec.Unschedulable {
			nodesToDrain = append(nodesToDrain, node)
		}
	}

	if len(nodesToDrain) == 0 {
		return 0, nil
	}

	results := s.drainNodes(ctx, plan, nodesToDrain, maxParallel)
	return s.applyDrainResults(ctx, plan, results)
}

// applyDrainResults processes drain outcomes, updates NodeStatus and conditions,
// fires events, and returns the appropriate requeue duration.
func (s *MaintenanceService) applyDrainResults(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, results []drainNodeResult) (time.Duration, error) {
	original := plan.DeepCopy()

	allDone := true
	evictionsInFlight := false
	anyBlocked := false

	for _, r := range results {
		var blocked *drainBlockedError
		switch {
		case r.Err == nil && r.Outcome.Evicted == 0:
			// Node is fully drained — nothing evictable found.
			updateNodeDrainStatus(plan, r.NodeName, r.Outcome, nil)

		case r.Err == nil && r.Outcome.Evicted > 0:
			// Evictions fired; pods are terminating.
			allDone = false
			evictionsInFlight = true
			updateNodeDrainStatus(plan, r.NodeName, r.Outcome, nil)
			s.log.Info("evictions in progress", "node", r.NodeName, "evicted", r.Outcome.Evicted)

		case errors.As(r.Err, &blocked):
			allDone = false
			anyBlocked = true
			issues := blockedPodIssues(blocked)
			updateNodeDrainStatus(plan, r.NodeName, r.Outcome, issues)
			s.log.Info("drain blocked", "node", r.NodeName, "reason", blocked.Error())
			s.recorder.Eventf(plan, "Warning", "DrainBlocked", "node %q: %s", r.NodeName, blocked.Error())

		default:
			// Unexpected error.
			allDone = false
			updateNodeDrainStatus(plan, r.NodeName, r.Outcome, nil)
			s.log.Error(r.Err, "drain error", "node", r.NodeName)
			s.recorder.Eventf(plan, "Warning", "DrainFailed", "node %q: %v", r.NodeName, r.Err)
		}
	}

	// Set conditions reflecting the aggregate drain state.
	if allDone {
		setCondition(plan, v1alpha1.ConditionDrainSucceeded, metav1.ConditionTrue,
			"AllPodsEvicted", "All target pods have been evicted")
		setCondition(plan, v1alpha1.ConditionDrainInProgress, metav1.ConditionFalse,
			"Idle", "Drain is not in progress")
		setCondition(plan, v1alpha1.ConditionDrainBlocked, metav1.ConditionFalse,
			"Cleared", "No blocking issues")
	} else if anyBlocked && !evictionsInFlight {
		// All nodes blocked, none making progress — user action likely required.
		setCondition(plan, v1alpha1.ConditionDrainBlocked, metav1.ConditionTrue,
			"PodBlocked", "One or more pods cannot be evicted")
		setCondition(plan, v1alpha1.ConditionDrainInProgress, metav1.ConditionFalse,
			"Blocked", "Drain is blocked and not making progress")
		setCondition(plan, v1alpha1.ConditionDrainSucceeded, metav1.ConditionFalse,
			"Blocked", "Drain has not succeeded yet")
	} else {
		// Evictions in progress, possibly with some blocks too.
		setCondition(plan, v1alpha1.ConditionDrainInProgress, metav1.ConditionTrue,
			"Evicting", "Pod evictions in progress")
		setCondition(plan, v1alpha1.ConditionDrainSucceeded, metav1.ConditionFalse,
			"InProgress", "Drain has not succeeded yet")
		if anyBlocked {
			setCondition(plan, v1alpha1.ConditionDrainBlocked, metav1.ConditionTrue,
				"PDBBlocked", "One or more pods are blocked by PodDisruptionBudgets")
		} else {
			setCondition(plan, v1alpha1.ConditionDrainBlocked, metav1.ConditionFalse,
				"Cleared", "No blocking issues")
		}
	}

	if err := s.client.Status().Patch(ctx, plan, client.MergeFrom(original)); err != nil {
		return 0, fmt.Errorf("patching drain status: %w", err)
	}

	if allDone {
		s.recorder.Eventf(plan, corev1.EventTypeNormal, "DrainSucceeded", "all target pods evicted from managed nodes")
		return 0, nil
	}
	if evictionsInFlight {
		return drainCheckInterval, nil
	}
	return drainBlockedRetry, nil
}

// drainNodes fans out drainNode calls across nodes with maxParallel concurrency.
func (s *MaintenanceService) drainNodes(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, nodes []*corev1.Node, maxParallel int) []drainNodeResult {
	results := make([]drainNodeResult, len(nodes))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node *corev1.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outcome, err := s.drainNode(ctx, plan, node)
			results[i] = drainNodeResult{
				NodeName: node.Name,
				Outcome:  outcome,
				Err:      err,
			}
		}(i, node)
	}

	wg.Wait()
	return results
}

// updateNodeDrainStatus writes drain counters and issues into the matching
// NodeStatus entry. If no entry exists for the node it is left unchanged.
func updateNodeDrainStatus(plan *v1alpha1.NodeMaintenancePlan, nodeName string, outcome drainOutcome, issues []v1alpha1.NodeIssue) {
	for i := range plan.Status.Nodes {
		if plan.Status.Nodes[i].Name != nodeName {
			continue
		}
		ns := &plan.Status.Nodes[i]
		ns.TotalPods = int32(outcome.Total)
		ns.EvictablePods = int32(outcome.Evictable)
		ns.Issues = issues
		return
	}
}

// blockedPodIssues converts a drainBlockedError into NodeIssue entries.
func blockedPodIssues(blocked *drainBlockedError) []v1alpha1.NodeIssue {
	issues := make([]v1alpha1.NodeIssue, 0, len(blocked.pods))
	for _, pod := range blocked.pods {
		issueType := "ConfigBlocked"
		msg := "pod cannot be evicted: set force=true or deleteEmptyDirData=true to proceed"
		if blocked.pdbBlocked {
			issueType = "PDBBlocked"
			msg = "pod eviction rejected by PodDisruptionBudget"
		}
		issues = append(issues, v1alpha1.NodeIssue{
			Type:    issueType,
			Message: msg,
			PodRef:  &v1alpha1.PodReference{Namespace: pod.Namespace, Name: pod.Name},
		})
	}
	return issues
}
