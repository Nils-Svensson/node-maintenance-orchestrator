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

// +kubebuilder:object:generate=true
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//
// FINALIZER
//

const NodeMaintenancePlanFinalizer string = "maintenance.nmoo.io/finalizer"

//
// CONDITION TYPES
//

const (
	ConditionNodesSelected   = "NodesSelected"
	ConditionScheduled       = "Scheduled"
	ConditionCordoned        = "Cordoned"
	ConditionDrainInProgress = "DrainInProgress"
	ConditionDrainSucceeded  = "DrainSucceeded"
	ConditionDrainBlocked    = "DrainBlocked"
	ConditionConflict        = "ConflictDetected"
	ConditionDriftDetected   = "DriftDetected"
)

//
// SPEC STRUCTS
//

// CordonSpec defines cordon behavior
type CordonSpec struct {
	// Whether cordon should be applied
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Time at which cordon should begin
	// +optional
	StartAt *metav1.Time `json:"startAt,omitempty"`
}

// DrainOptions defines behavior for draining
type DrainOptions struct {
	// Max number of nodes drained in parallel
	// +kubebuilder:default=1
	MaxParallel int32 `json:"maxParallel,omitempty"`

	// Ignore DaemonSet-managed pods
	// +kubebuilder:default=true
	IgnoreDaemonSets bool `json:"ignoreDaemonSets,omitempty"`

	// Whether to delete pods with emptyDir volumes.
	// Defaults to false
	DeleteEmptyDirData bool `json:"deleteEmptyDirData,omitempty"`

	// Force delete pods not backed by controllers
	// +kubebuilder:default=false
	Force bool `json:"force,omitempty"`

	// Timeout for pod eviction (seconds)
	// +kubebuilder:validation:Minimum=0
	// +optional
	TimeoutSeconds int64 `json:"timeoutSeconds,omitempty"`

	// Whether to honour PodDisruptionBudgets during eviction. When false, pods
	// blocked by a PDB are force-deleted via the Delete API instead of the
	// Eviction API, bypassing budget checks entirely.
	// +kubebuilder:default=true
	RespectPodDisruptionBudgets bool `json:"respectPodDisruptionBudgets,omitempty"`
}

// DrainSpec defines drain behavior
type DrainSpec struct {
	// Whether draining should be performed
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Time at which drain should begin
	// +optional
	StartAt *metav1.Time `json:"startAt,omitempty"`

	// Drain options
	// +optional
	Options *DrainOptions `json:"options,omitempty"`
}

// NodeMaintenancePlanSpec defines desired state
// +kubebuilder:validation:XValidation:rule="!has(self.cordon) || !has(self.drain) || !has(self.cordon.startAt) || !has(self.drain.startAt) || self.cordon.startAt <= self.drain.startAt",message="cordon.startAt must be before or equal to drain.startAt"
// +kubebuilder:validation:XValidation:rule="!(has(self.nodes) && has(self.nodeSelector))",message="cannot set both nodes and nodeSelector"
// +kubebuilder:validation:XValidation:rule="!has(self.drain) || !self.drain.enabled || (has(self.cordon) && self.cordon.enabled)",message="drain requires cordon to be enabled"
type NodeMaintenancePlanSpec struct {

	// TODO: The operator is currently "cooperative", in that it doesn't aggressively enforce the plan spec against certain external actions (e.g. manual uncordon).
	// Maybe add enforcementPolicy option to let users choose between "cooperative" and "authoritative" modes? 
	// Authoritative mode would involve the operator reverting any manual changes that conflict with the plan spec.
	//
	// Explicit list of node names
	// Mutually exclusive with NodeSelector
	// +optional
	Nodes []string `json:"nodes,omitempty"`

	// Label selector for nodes
	// Mutually exclusive with Nodes
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// Reason for maintenance (added as annotation to nodes)
	// +optional
	Reason string `json:"reason,omitempty"`

	// Cordon behavior
	// +optional
	Cordon *CordonSpec `json:"cordon,omitempty"`

	// Drain behavior
	// +optional
	Drain *DrainSpec `json:"drain,omitempty"`
}

//
// STATUS STRUCTS
//

// PodReference identifies a pod
type PodReference struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// NodeIssue represents a detected issue
type NodeIssue struct {
	// Type of issue (e.g. PDBBlocked, Unschedulable, DriftDetected)
	Type string `json:"type"`

	// Human-readable message
	Message string `json:"message"`

	// Related pod if applicable
	// +optional
	PodRef *PodReference `json:"podRef,omitempty"`
}

// NodeDrainPreview holds a dry-run analysis of what drain would do to a node.
// Computed before drain starts and frozen once DrainInProgress becomes true.
type NodeDrainPreview struct {
	// Number of pods that will be evicted with current drain settings.
	// +optional
	EvictableCount int32 `json:"evictableCount,omitempty"`

	// Number of pods that will be skipped (DaemonSets, mirror pods, completed pods).
	// +optional
	SkippedCount int32 `json:"skippedCount,omitempty"`

	// Issues lists pods that will block or may complicate drain.
	// An empty list means no issues were detected at preview time.
	// +optional
	Issues []NodeIssue `json:"issues,omitempty"`

	// When this preview was last computed.
	// +optional
	ComputedAt *metav1.Time `json:"computedAt,omitempty"`
}

// NodeStatus represents per-node observed state
type NodeStatus struct {
	Name string `json:"name"`

	Cordoned bool `json:"cordoned,omitempty"`

	// +optional
	CordonedAt *metav1.Time `json:"cordonedAt,omitempty"`

	DrainProgress int32 `json:"drainProgress,omitempty"`

	Drifted bool `json:"drifted,omitempty"`

	DriftReason string `json:"driftReason,omitempty"`

	// Counter for draining progress and pod classification. TotalPods is the total number
	// of pods on the node calculated on each reconciliation loop.
	TotalPods int32 `json:"totalPods,omitempty"`

	// InitialPodCount is the number of pods on the node at the time it was first cordoned or marked for maintenance.
	// This serves as a baseline for tracking drain progress.
	InitialPodCount int32 `json:"initialPodCount,omitempty"`

	EvictablePods int32 `json:"evictablePods,omitempty"`

	BlockedPods int32 `json:"blockedPods,omitempty"`

	UnschedulablePods int32 `json:"unschedulablePods,omitempty"`

	EvictedTotal int32 `json:"evictedTotal,omitempty"`

	// Dry-run analysis computed before drain starts.
	// +optional
	DrainPreview *NodeDrainPreview `json:"drainPreview,omitempty"`

	// Issues detected during drain execution.
	// +optional
	Issues []NodeIssue `json:"issues,omitempty"`

	// True when adpoted+cordoned+drained are all satisfied. I should figure out if adpoted
	// + drained, with cordon enabled=false is a valid state that should be considered ready for maintenance.
	ReadyForMaintenance bool `json:"readyForMaintenance,omitempty"`
}

// NodeMaintenancePlanStatus defines observed state
type NodeMaintenancePlanStatus struct {
	// ResolvedNodes holds the snapshot of node names selected when this plan uses
	// NodeSelector. Populated on the first reconcile and never expanded thereafter,
	// so nodes added to the cluster after plan creation are not automatically adopted.
	// +optional
	ResolvedNodes []string `json:"resolvedNodes,omitempty"`

	// NodeSnapshotTaken is true once the NodeSelector snapshot has been taken.
	// Distinguishes "snapshot taken and empty" from "snapshot not yet taken".
	// +optional
	NodeSnapshotTaken bool `json:"nodeSnapshotTaken,omitempty"`
	// Conditions represent current state of the plan
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Per-node status
	// +optional
	Nodes []NodeStatus `json:"nodes,omitempty"`

	// List of nodes with detected drift
	// +optional
	DriftedNodes []string `json:"driftedNodes,omitempty"`

	// Last time preview was computed
	// +optional
	LastPreviewTime *metav1.Time `json:"lastPreviewTime,omitempty"`

	// Total evictable pods across all nodes at last preview.
	// +optional
	TotalEvictablePods int32 `json:"totalEvictablePods,omitempty"`

	// Total number of preview issues detected across all nodes at last preview.
	// +optional
	TotalPreviewIssues int32 `json:"totalPreviewIssues,omitempty"`

	// Last error message if preview or execution failed
	// +optional
	LastError string `json:"lastError,omitempty"`

	// ObservedGeneration reflects latest reconciled spec
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// True when all nodes have status readyForMaintenance = true
	// +optional
	AllNodesReadyForMaintenance bool `json:"allNodesReadyForMaintenance,omitempty"`
}

//
// ROOT OBJECT
//

// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=nmp
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=".status.nodes.size()"
// +kubebuilder:printcolumn:name="Cordoned",type=string,JSONPath=".status.conditions[?(@.type=='Cordoned')].status"
// +kubebuilder:printcolumn:name="Draining",type=string,JSONPath=".status.conditions[?(@.type=='DrainInProgress')].status"
// +kubebuilder:printcolumn:name="Drift",type=integer,JSONPath=".status.driftedNodes"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type NodeMaintenancePlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeMaintenancePlanSpec   `json:"spec,omitempty"`
	Status NodeMaintenancePlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
type NodeMaintenancePlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeMaintenancePlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeMaintenancePlan{}, &NodeMaintenancePlanList{})
}
