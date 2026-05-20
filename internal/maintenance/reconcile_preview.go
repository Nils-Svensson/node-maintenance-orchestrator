package maintenance

import (
	"context"
	"fmt"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcilePreview computes a dry-run drain analysis for each managed node and
// writes the results to NodeStatus.DrainPreview. It runs on every reconcile
// pass until drain starts (DrainInProgress=True), at which point live execution
// data supersedes the preview and further computation is skipped.
func (s *MaintenanceService) ReconcilePreview(ctx context.Context, plan *v1alpha1.NodeMaintenancePlan, res *OwnershipResolution) error {
	cfg := getDrainConfig(plan)
	if cfg == nil {
		return nil
	}

	if isDrainInProgress(plan) {
		return nil
	}

	// Preview nodes we intend to manage: already owned (Stable) and pending adoption (ToAdopt).
	targetNodes := make([]*corev1.Node, 0, len(res.Stable)+len(res.ToAdopt))
	targetNodes = append(targetNodes, res.Stable...)
	targetNodes = append(targetNodes, res.ToAdopt...)

	if len(targetNodes) == 0 {
		return nil
	}

	original := plan.DeepCopy()
	now := metav1.Now()

	var totalEvictable, totalIssues int32

	for _, node := range targetNodes {
		filterResult, err := s.filterPodsForDrain(ctx, node, cfg)
		if err != nil {
			return fmt.Errorf("preview: filtering pods on node %q: %w", node.Name, err)
		}

		configIssues := buildConfigIssues(filterResult.Blocked)

		pdbIssues, err := s.buildPDBIssues(ctx, filterResult.Evictable)
		if err != nil {
			return fmt.Errorf("preview: checking PDBs for node %q: %w", node.Name, err)
		}

		allIssues := append(configIssues, pdbIssues...)

		setNodePreview(plan, node.Name, &v1alpha1.NodeDrainPreview{
			EvictableCount: int32(len(filterResult.Evictable)),
			SkippedCount:   int32(len(filterResult.Skipped)),
			Issues:         allIssues,
			ComputedAt:     &now,
		})

		totalEvictable += int32(len(filterResult.Evictable))
		totalIssues += int32(len(allIssues))
	}

	plan.Status.TotalEvictablePods = totalEvictable
	plan.Status.TotalPreviewIssues = totalIssues
	plan.Status.LastPreviewTime = &now

	return s.client.Status().Patch(ctx, plan, client.MergeFrom(original))
}

// isDrainInProgress returns true when the DrainInProgress condition is True,
// indicating that evictions have started and preview is no longer meaningful.
func isDrainInProgress(plan *v1alpha1.NodeMaintenancePlan) bool {
	for _, c := range plan.Status.Conditions {
		if c.Type == v1alpha1.ConditionDrainInProgress {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// setNodePreview writes the preview into the matching NodeStatus entry.
func setNodePreview(plan *v1alpha1.NodeMaintenancePlan, nodeName string, preview *v1alpha1.NodeDrainPreview) {
	for i := range plan.Status.Nodes {
		if plan.Status.Nodes[i].Name == nodeName {
			plan.Status.Nodes[i].DrainPreview = preview
			return
		}
	}
}

// buildConfigIssues converts config-blocked pods into NodeIssue entries.
// These are deterministic: they will block drain unless the plan settings change.
func buildConfigIssues(blocked []corev1.Pod) []v1alpha1.NodeIssue {
	issues := make([]v1alpha1.NodeIssue, 0, len(blocked))
	for i := range blocked {
		pod := &blocked[i]
		issue := v1alpha1.NodeIssue{
			PodRef: &v1alpha1.PodReference{Namespace: pod.Namespace, Name: pod.Name},
		}
		switch {
		case isDaemonSetPod(pod):
			issue.Type = "DaemonSetPod"
			issue.Message = "DaemonSet pod cannot be evicted; set ignoreDaemonSets: true to skip"
		case !hasController(pod):
			issue.Type = "UncontrolledPod"
			issue.Message = "pod has no owner and force is not enabled; set force: true to evict"
		case hasEmptyDir(pod):
			issue.Type = "EmptyDirVolume"
			issue.Message = "pod has an emptyDir volume and deleteEmptyDirData is not enabled; set deleteEmptyDirData: true to evict"
		default:
			issue.Type = "BlockedPod"
			issue.Message = "pod cannot be evicted with current drain settings"
		}
		issues = append(issues, issue)
	}
	return issues
}

// buildPDBIssues checks evictable pods against PodDisruptionBudgets that
// currently have no disruptions available. These are snapshot warnings —
// the budget may change before drain runs.
func (s *MaintenanceService) buildPDBIssues(ctx context.Context, pods []corev1.Pod) ([]v1alpha1.NodeIssue, error) {
	if len(pods) == 0 {
		return nil, nil
	}

	// Group pods by namespace to minimise List calls.
	byNS := make(map[string][]corev1.Pod)
	for _, pod := range pods {
		byNS[pod.Namespace] = append(byNS[pod.Namespace], pod)
	}

	var issues []v1alpha1.NodeIssue
	for ns, nsPods := range byNS {
		var pdbList policyv1.PodDisruptionBudgetList
		if err := s.client.List(ctx, &pdbList, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("listing PDBs in namespace %q: %w", ns, err)
		}

		for i := range nsPods {
			pod := &nsPods[i]
			for j := range pdbList.Items {
				pdb := &pdbList.Items[j]
				if pdb.Status.DisruptionsAllowed > 0 || pdb.Spec.Selector == nil {
					continue
				}
				sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
				if err != nil {
					continue
				}
				if !sel.Matches(labels.Set(pod.Labels)) {
					continue
				}
				issues = append(issues, v1alpha1.NodeIssue{
					Type: "PDBActive",
					Message: fmt.Sprintf(
						"covered by PDB %q (0 disruptions available at preview time; may change before drain runs)",
						pdb.Name,
					),
					PodRef: &v1alpha1.PodReference{Namespace: pod.Namespace, Name: pod.Name},
				})
				break // one PDB issue per pod is sufficient
			}
		}
	}
	return issues, nil
}
