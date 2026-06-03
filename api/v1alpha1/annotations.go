package v1alpha1

const (
	// ManagedByAnnotation is set on a node to indicate which NodeMaintenancePlan owns it.
	ManagedByAnnotation = "maintenance.nmoo.io/managed-by"

	// ReasonAnnotation carries the maintenance reason on the node.
	ReasonAnnotation = "maintenance.nmoo.io/reason"

	// CordonedAnnotation is set on a node when the operator has cordoned it.
	// Drift detection uses this to distinguish "never cordoned by the operator"
	// from "operator cordoned it and someone manually undid that."
	CordonedAnnotation = "maintenance.nmoo.io/cordoned"

	// InMaintenanceLabel is set on a node as a label while it is under management
	// by any NodeMaintenancePlan. External operators (e.g. cluster-autoscaler) can
	// use this label as a selector to avoid scheduling work onto nodes mid-maintenance.
	InMaintenanceLabel = "maintenance.nmoo.io/in-maintenance"
)
