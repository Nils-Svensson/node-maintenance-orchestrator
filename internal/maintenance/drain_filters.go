package maintenance

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// drainFilterResult categorises pods on a node into four buckets.
// Evictable will be evicted. Blocked prevent drain from proceeding.
// Terminating have already been evicted and are awaiting removal.
// Skipped are intentionally left alone (mirror pods, DaemonSets, completed pods).
type drainFilterResult struct {
	Evictable   []corev1.Pod
	Blocked     []corev1.Pod
	Terminating []corev1.Pod
	Skipped     []corev1.Pod
}

// filterPodsForDrain lists all pods on the node and classifies them.
func (s *MaintenanceService) filterPodsForDrain(ctx context.Context, node *corev1.Node, cfg *drainConfig) (*drainFilterResult, error) {
	var podList corev1.PodList
	if err := s.client.List(ctx, &podList, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return nil, fmt.Errorf("listing pods on node %s: %w", node.Name, err)
	}
	return classifyPods(podList.Items, cfg), nil
}

// classifyPods is a pure function that categorises a pod slice according to cfg.
func classifyPods(pods []corev1.Pod, cfg *drainConfig) *drainFilterResult {
	result := &drainFilterResult{}
	for i := range pods {
		pod := &pods[i]

		// Always skip: mirror pods — they represent static pods and cannot be evicted.
		if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
			result.Skipped = append(result.Skipped, *pod)
			continue
		}

		// Already-terminating pods: eviction was issued previously. Count separately
		// so drain does not report completion until the node is physically empty.
		if pod.DeletionTimestamp != nil {
			result.Terminating = append(result.Terminating, *pod)
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
