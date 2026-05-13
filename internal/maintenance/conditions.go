// conditions.go contains helper utilities for managing Kubernetes conditions
// on the NodeMaintenancePlan status.
//
// It provides standardized methods for setting, updating, and querying
// metav1.Condition entries, ensuring consistent condition handling across
// the controller.
//
// This abstraction avoids duplication and enforces best practices for
// condition management.

package maintenance
