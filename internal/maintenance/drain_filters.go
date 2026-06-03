package maintenance

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// drainFilterResult categorises pods on a node into five buckets.
// Evictable will be evicted. Blocked prevent drain from proceeding.
// Terminating have already been evicted and are awaiting removal within their grace period.
// StuckTerminating have exceeded their grace period and are no longer making progress.
// Skipped are intentionally left alone (mirror pods, DaemonSets, completed pods).
type drainFilterResult struct {
	Evictable        []corev1.Pod
	Blocked          []corev1.Pod
	Terminating      []corev1.Pod
	StuckTerminating []corev1.Pod
	Skipped          []corev1.Pod
}

// stuckTerminatingBuffer is the additional time beyond a pod's
// terminationGracePeriodSeconds before it is considered stuck. Accounts for
// kubelet and API server propagation latency.
const stuckTerminatingBuffer = 60 * time.Second

// filterPodsForDrain lists all pods on the node and classifies them.
func (s *MaintenanceService) filterPodsForDrain(ctx context.Context, node *corev1.Node, cfg *drainConfig) (*drainFilterResult, error) {
	var podList corev1.PodList
	if err := s.client.List(ctx, &podList, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return nil, fmt.Errorf("listing pods on node %s: %w", node.Name, err)
	}
	return classifyPods(podList.Items, cfg, s.clock.Now()), nil
}

// classifyPods is a pure function that categorises a pod slice according to cfg.
// now is used to detect pods stuck in terminating state.
func classifyPods(pods []corev1.Pod, cfg *drainConfig, now time.Time) *drainFilterResult {
	result := &drainFilterResult{}
	for i := range pods {
		pod := &pods[i]

		// Always skip: mirror pods — they represent static pods and cannot be evicted.
		if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
			result.Skipped = append(result.Skipped, *pod)
			continue
		}

		// Terminating pods: eviction was issued previously. Distinguish between pods
		// still within their grace period and those that have exceeded it and are stuck.
		if pod.DeletionTimestamp != nil {
			gracePeriod := int64(30)
			if pod.Spec.TerminationGracePeriodSeconds != nil {
				gracePeriod = *pod.Spec.TerminationGracePeriodSeconds
			}
			deadline := pod.DeletionTimestamp.Add(
				time.Duration(gracePeriod)*time.Second + stuckTerminatingBuffer,
			)
			if now.After(deadline) {
				result.StuckTerminating = append(result.StuckTerminating, *pod)
			} else {
				result.Terminating = append(result.Terminating, *pod)
			}
			continue
		}

		// Always skip: already terminated pods.
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			result.Skipped = append(result.Skipped, *pod)
			continue
		}

		// DaemonSet pods are node-bound and cannot be meaningfully rescheduled.
		if isDaemonSetPod(pod) {
			if cfg.IgnoreDaemonSets {
				result.Skipped = append(result.Skipped, *pod)
			} else {
				result.Blocked = append(result.Blocked, *pod)
			}
			continue
		}

		// Uncontrolled pods (no owning controller) will not be rescheduled; block
		// unless Force is explicitly set.
		if !hasController(pod) && !cfg.Force {
			result.Blocked = append(result.Blocked, *pod)
			continue
		}

		// Pods with emptyDir volumes will lose that data on eviction; block unless
		// the operator has been told data loss is acceptable.
		if hasEmptyDir(pod) && !cfg.DeleteEmptyDirData {
			result.Blocked = append(result.Blocked, *pod)
			continue
		}

		result.Evictable = append(result.Evictable, *pod)
	}
	return result
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// hasController returns true if any owner reference is marked as the controlling owner.
func hasController(pod *corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func hasEmptyDir(pod *corev1.Pod) bool {
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil {
			return true
		}
	}
	return false
}
