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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//
// FINALIZER
//

const NodeMaintenancePlanFinalizer = "maintenance.mnoo.io/finalizer"

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

	// Ignore pods managed by StatefulSets
	// +kubebuilder:default=false
	IgnoreStatefulSets bool `json:"ignoreStatefulSets,omitempty"`

	// Ignore pod disruption budgets
	// +kubebuilder:default=false
	IgnorePDBs bool `json:"ignorePDBs,omitempty"`

	// Force delete pods not backed by controllers
	// +kubebuilder:default=false
	Force bool `json:"force,omitempty"`

	// Timeout for pod eviction (seconds)
	// +optional
	TimeoutSeconds int64 `json:"timeoutSeconds,omitempty"`
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
type NodeMaintenancePlanSpec struct {
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

// NodeStatus represents per-node observed state
type NodeStatus struct {
	Name string `json:"name"`

	Cordoned bool `json:"cordoned,omitempty"`

	DrainProgress int32 `json:"drainProgress,omitempty"`

	TotalPods         int32 `json:"totalPods,omitempty"`
	EvictablePods     int32 `json:"evictablePods,omitempty"`
	BlockedPods       int32 `json:"blockedPods,omitempty"`
	UnschedulablePods int32 `json:"unschedulablePods,omitempty"`

	// Issues detected during preview or execution
	// +optional
	Issues []NodeIssue `json:"issues,omitempty"`
}

// NodeMaintenancePlanStatus defines observed state
type NodeMaintenancePlanStatus struct {
	// Conditions represent current state of the plan
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Per-node status
	// +optional
	Nodes []NodeStatus `json:"nodes,omitempty"`

	// Number of nodes with detected drift
	// +optional
	DriftedNodes int32 `json:"driftedNodes,omitempty"`

	// Last time preview was computed
	// +optional
	LastPreviewTime *metav1.Time `json:"lastPreviewTime,omitempty"`

	// Last error message if preview or execution failed
	// +optional
	LastError string `json:"lastError,omitempty"`

	// ObservedGeneration reflects latest reconciled spec
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//
// ROOT OBJECT
//

// +kubebuilder:object:root=true
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

type NodeMaintenancePlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeMaintenancePlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeMaintenancePlan{}, &NodeMaintenancePlanList{})
}