# Events and Conditions

Events and conditions are both emitted on the `NodeMaintenancePlan` object. Conditions persist and reflect current state; events are a timestamped audit trail.

```bash
kubectl describe nmp <name>
```

---

## Conditions

| Type | Meaning |
|------|---------|
| `NodesSelected` | At least one node is under management by this plan. |
| `Scheduled` | Nodes are adopted but `cordon.startAt` is still in the future. |
| `Cordoned` | All non-drifted managed nodes are unschedulable. |
| `DrainInProgress` | Pod evictions are in flight or terminating pods have not yet been removed. |
| `DrainSucceeded` | All target pods have been evicted and physically removed from the nodes. |
| `DrainBlocked` | One or more pods cannot be evicted (PDB or configuration). |
| `DrainTimedOut` | Drain did not complete within `drain.timeoutMinutes`. Terminal — the operator stops retrying. |
| `ConflictDetected` | One or more nodes in the plan are already owned by another plan. |
| `DriftDetected` | At least one managed node has diverged from the desired cordon state. |

### Condition notes

**`Cordoned`** excludes drifted nodes from its check. A plan with one drifted node and three correctly cordoned nodes will have `Cordoned=True`.

**`DrainBlocked` and `DrainInProgress`** can both be true simultaneously when some nodes are actively evicting pods while others are stuck on a PDB.

**`DrainTimedOut`** is the only terminal failure condition. Once set, it is not cleared by the operator. Delete and recreate the plan to retry.

---

## Events

| Reason | Type | Description |
|--------|------|-------------|
| `NodeAdopted` | Normal | Node brought under management by this plan. |
| `NodeCordoned` | Normal | Node marked unschedulable by the operator. |
| `NodeUncordoned` | Normal | Node returned to schedulable (cordon disabled or plan deleted). |
| `NodeReleased` | Normal | Node removed from management; annotations cleared. |
| `NodeDrained` | Normal | Node has no remaining evictable pods. |
| `NodeReadyForMaintenance` | Normal | Node is cordoned and fully drained. Physical maintenance can begin. |
| `AllNodesReadyForMaintenance` | Normal | All nodes in the plan are ready for maintenance. |
| `MaintenanceComplete` | Normal | Node was uncordoned after `readyForMaintenance=true`. Ownership released cleanly. |
| `OwnershipConflict` | Warning | Node is already managed by another plan. This plan will not manage it. |
| `DriftDetected` | Warning | Node state diverged from the plan. See [Drift handling](drift-handling.md). |
| `DrainBlocked` | Warning | A pod eviction was rejected by a PodDisruptionBudget or blocked by plan configuration. |
| `DrainFailed` | Warning | Unexpected error during pod eviction. |
| `DrainTimedOut` | Warning | Drain deadline exceeded. |

---

## Prometheus metrics

The operator exposes the following custom metrics in addition to the standard controller-runtime metrics.

### Plan-level

| Metric | Labels | Description |
|--------|--------|-------------|
| `nmo_plan_managed_nodes_total` | `plan` | Total nodes under management. |
| `nmo_plan_ready_nodes_total` | `plan` | Nodes that have reached `readyForMaintenance`. |
| `nmo_plan_draining_nodes_total` | `plan` | Nodes currently draining. |
| `nmo_plan_blocked_nodes_total` | `plan` | Nodes with at least one pod blocking drain. |
| `nmo_plan_drifted_nodes_total` | `plan` | Nodes that have drifted from desired state. |
| `nmo_plan_drain_progress` | `plan` | Average drain completion ratio (0–1). |
| `nmo_plan_phase` | `plan`, `phase` | 1 for the active phase, 0 for all others. |

### Node-level

| Metric | Labels | Description |
|--------|--------|-------------|
| `nmo_node_drain_progress` | `plan`, `node` | Per-node drain completion ratio (0–1). |
| `nmo_node_evicted_pods_total` | `plan`, `node` | Cumulative pods evicted by this plan. |
| `nmo_node_blocked_pods` | `plan`, `node` | Pods currently blocking drain. |

### Example alert rules

```yaml
# Any plan stuck in Blocked phase for more than 30 minutes
- alert: MaintenanceDrainBlocked
  expr: nmo_plan_phase{phase="Blocked"} == 1
  for: 30m
  annotations:
    summary: "Drain blocked on plan {{ $labels.plan }}"

# Any plan that has timed out
- alert: MaintenanceDrainTimedOut
  expr: nmo_plan_phase{phase="TimedOut"} == 1
  annotations:
    summary: "Drain timed out on plan {{ $labels.plan }}"

# Nodes have drifted
- alert: MaintenanceNodeDrifted
  expr: nmo_plan_drifted_nodes_total > 0
  annotations:
    summary: "{{ $value }} node(s) drifted on plan {{ $labels.plan }}"
```
