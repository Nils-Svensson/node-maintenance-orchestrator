package metrics

import (
	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// allPhases lists every phase value computePhase can return, so we can
// zero out stale phase entries when a plan transitions to a new phase.
var allPhases = []string{
	"Pending", "Adopted", "Scheduled", "Cordoned",
	"Draining", "Blocked", "Ready", "TimedOut", "Conflict",
}

var (
	planManagedNodes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_managed_nodes_total",
		Help: "Total number of nodes currently under management by the plan.",
	}, []string{"plan"})

	planReadyNodes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_ready_nodes_total",
		Help: "Number of nodes that have reached ReadyForMaintenance.",
	}, []string{"plan"})

	planDrainingNodes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_draining_nodes_total",
		Help: "Number of nodes currently being drained.",
	}, []string{"plan"})

	planBlockedNodes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_blocked_nodes_total",
		Help: "Number of nodes with at least one pod blocking drain.",
	}, []string{"plan"})

	planDriftedNodes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_drifted_nodes_total",
		Help: "Number of nodes that have drifted from the desired cordon state.",
	}, []string{"plan"})

	planDrainProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_drain_progress",
		Help: "Average drain completion ratio across all managed nodes (0–1).",
	}, []string{"plan"})

	// planPhase uses the label-per-state pattern: 1 for the active phase, 0 for
	// all others. This lets alerting rules select on the phase label directly,
	// e.g. nmo_plan_phase{phase="TimedOut"} == 1.
	planPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_plan_phase",
		Help: "Current lifecycle phase of the plan. 1 for the active phase, 0 for all others.",
	}, []string{"plan", "phase"})

	nodeDrainProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_node_drain_progress",
		Help: "Per-node drain completion ratio (0–1).",
	}, []string{"plan", "node"})

	nodeEvictedTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_node_evicted_pods_total",
		Help: "Cumulative number of pods evicted from the node by this plan.",
	}, []string{"plan", "node"})

	nodeBlockedPods = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nmo_node_blocked_pods",
		Help: "Number of pods currently blocking drain on this node.",
	}, []string{"plan", "node"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		planManagedNodes,
		planReadyNodes,
		planDrainingNodes,
		planBlockedNodes,
		planDriftedNodes,
		planDrainProgress,
		planPhase,
		nodeDrainProgress,
		nodeEvictedTotal,
		nodeBlockedPods,
	)
}

// RecordPlan updates all metrics for plan from its current status.
// Called at the end of each successful reconcile.
func RecordPlan(plan *v1alpha1.NodeMaintenancePlan) {
	name := plan.Name

	var ready, draining, blocked, drifted int32
	var totalProgress int32
	for _, ns := range plan.Status.Nodes {
		totalProgress += ns.DrainProgress
		if ns.ReadyForMaintenance {
			ready++
		} else if ns.TotalPods > 0 {
			draining++
		}
		if ns.BlockedPods > 0 {
			blocked++
		}
		if ns.Drifted {
			drifted++
		}
	}

	planManagedNodes.WithLabelValues(name).Set(float64(plan.Status.NodeCount))
	planReadyNodes.WithLabelValues(name).Set(float64(ready))
	planDrainingNodes.WithLabelValues(name).Set(float64(draining))
	planBlockedNodes.WithLabelValues(name).Set(float64(blocked))
	planDriftedNodes.WithLabelValues(name).Set(float64(drifted))

	var progressRatio float64
	if plan.Status.NodeCount > 0 {
		progressRatio = float64(totalProgress) / float64(plan.Status.NodeCount) / 100.0
	}
	planDrainProgress.WithLabelValues(name).Set(progressRatio)

	// Set 1 for the active phase, 0 for all others.
	for _, p := range allPhases {
		v := 0.0
		if p == plan.Status.Phase {
			v = 1.0
		}
		planPhase.WithLabelValues(name, p).Set(v)
	}

	// Per-node metrics.
	for _, ns := range plan.Status.Nodes {
		nodeDrainProgress.WithLabelValues(name, ns.Name).Set(float64(ns.DrainProgress) / 100.0)
		nodeEvictedTotal.WithLabelValues(name, ns.Name).Set(float64(ns.EvictedTotal))
		nodeBlockedPods.WithLabelValues(name, ns.Name).Set(float64(ns.BlockedPods))
	}
}

// DeletePlan removes all metric series associated with a plan that is being
// deleted. Call this during finalizer cleanup before the plan object is gone.
func DeletePlan(plan *v1alpha1.NodeMaintenancePlan) {
	name := plan.Name

	planManagedNodes.DeleteLabelValues(name)
	planReadyNodes.DeleteLabelValues(name)
	planDrainingNodes.DeleteLabelValues(name)
	planBlockedNodes.DeleteLabelValues(name)
	planDriftedNodes.DeleteLabelValues(name)
	planDrainProgress.DeleteLabelValues(name)
	for _, p := range allPhases {
		planPhase.DeleteLabelValues(name, p)
	}
	for _, ns := range plan.Status.Nodes {
		nodeDrainProgress.DeleteLabelValues(name, ns.Name)
		nodeEvictedTotal.DeleteLabelValues(name, ns.Name)
		nodeBlockedPods.DeleteLabelValues(name, ns.Name)
	}
}
