// preview.go implements dry-run analysis of node maintenance operations.
//
// It simulates the effects of draining nodes without mutating cluster state,
// providing insights into potential issues such as:
//   - PodDisruptionBudget violations
//   - Unschedulable pods
//   - Non-evictable workloads
//
// The preview output is used to populate the NodeMaintenancePlan status
// and guide user decisions before executing disruptive actions.

package maintenance