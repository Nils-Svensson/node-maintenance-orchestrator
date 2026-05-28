# API Reference

## NodeMaintenancePlan

Cluster-scoped. Short name: `nmp`.

```
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
```

---

## Spec

### Node selection

Exactly one of `nodes` or `nodeSelector` must be set.

| Field | Type | Description |
|-------|------|-------------|
| `nodes` | `[]string` | Explicit list of node names to manage. |
| `nodeSelector` | `LabelSelector` | Label selector. The matching node set is **snapshotted on first reconcile** and never expanded. Nodes added to the cluster after plan creation are not adopted. |
| `reason` | `string` | Free-text reason for maintenance. Added as the `maintenance.nmoo.io/reason` annotation on managed nodes. |

### `spec.cordon`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Mark selected nodes as unschedulable. |
| `startAt` | — | RFC 3339 time at which cordoning begins. Omit to cordon immediately on plan creation. |

### `spec.drain`

Requires `cordon.enabled: true`.

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Evict workloads from cordoned nodes. |
| `startAt` | — | RFC 3339 time at which drain begins. Omit to drain as soon as nodes are cordoned. |
| `timeoutMinutes` | — | Minutes before the plan is marked `DrainTimedOut`. Omit for no timeout. |
| `options` | — | See below. |

### `spec.drain.options`

All fields are optional. The defaults replicate `kubectl drain --ignore-daemonsets`.

| Field | Default | Description |
|-------|---------|-------------|
| `maxParallel` | `1` | Number of nodes drained concurrently. |
| `ignoreDaemonSets` | `true` | Skip DaemonSet-managed pods instead of blocking on them. |
| `deleteEmptyDirData` | `false` | Allow eviction of pods with `emptyDir` volumes. Data is lost on eviction. |
| `force` | `false` | Allow eviction of pods with no owning controller. These pods are not rescheduled. |
| `podTerminationGracePeriodSeconds` | — | Override each pod's own `terminationGracePeriodSeconds`. Set to `0` to force-kill immediately. Omit to use each pod's configured value. |
| `respectPodDisruptionBudgets` | `true` | When `false`, pods blocked by a PDB are force-deleted via the Delete API, bypassing budget checks entirely. |

### Valid configurations

| `cordon.enabled` | `drain.enabled` | Valid | Behavior |
|-----------------|----------------|-------|----------|
| `false` | `false` | ✓ | Nodes are adopted and tracked. No cordon or drain is applied. Useful for observing before enabling maintenance actions. |
| `true` | `false` | ✓ | Nodes are cordoned only. Useful when you want to block new scheduling but manage pod eviction yourself. |
| `true` | `true` | ✓ | Full maintenance preparation: nodes are cordoned, then drained. |
| `false` | `true` | ✗ | Rejected at admission. Drain requires cordon. |

---

## Status

### Plan-level fields

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | Human-readable lifecycle phase. See [Maintenance lifecycle](maintenance-lifecycle.md). |
| `readySummary` | `string` | `"ready/total"` fraction of nodes at `ReadyForMaintenance`. |
| `drainProgress` | `string` | Average drain completion across all nodes, e.g. `"67%"`. |
| `drainingNodeCount` | `string` | `"draining/total"` count of nodes actively draining. |
| `blockedNodeCount` | `string` | `"blocked/total"` count of nodes with at least one pod blocking drain. |
| `drifted` | `bool` | `true` when at least one managed node has drifted from the desired state. |
| `nodeCount` | `int32` | Total nodes currently under management. |
| `allNodesReadyForMaintenance` | `bool` | `true` when every managed node has `ReadyForMaintenance=true`. |
| `conditions` | `[]Condition` | Standard Kubernetes conditions. See [Events and conditions](events-and-conditions.md). |
| `nodes` | `[]NodeStatus` | Per-node observed state. |
| `resolvedNodes` | `[]string` | Snapshot of node names selected by `nodeSelector`. Populated once and never updated. |
| `nodeSnapshotTaken` | `bool` | `true` once the `nodeSelector` snapshot has been recorded. |
| `drainStartedAt` | `Time` | When the drain phase first became active. Used to enforce `timeoutMinutes`. |
| `observedGeneration` | `int64` | Generation of the spec last fully reconciled. |

### Per-node status (`status.nodes[]`)

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Node name. |
| `cordoned` | `bool` | Whether the node is currently unschedulable. |
| `cordonedAt` | `Time` | When the node was first cordoned. |
| `drainProgress` | `int32` | Drain completion percentage (0–100). |
| `drifted` | `bool` | Node has diverged from desired state. |
| `driftReason` | `string` | Reason for drift: `ManualUncordon`, `ExternalCordon`. |
| `totalPods` | `int32` | Total pods on the node at last reconcile. |
| `initialPodCount` | `int32` | Pod count when drain began. Used as the progress denominator. |
| `evictablePods` | `int32` | Pods eligible for eviction at last reconcile. |
| `blockedPods` | `int32` | Pods that cannot be evicted with current settings. |
| `evictedTotal` | `int32` | Cumulative pods evicted by this plan. |
| `readyForMaintenance` | `bool` | Node is cordoned and fully drained. |
| `issues` | `[]NodeIssue` | Issues detected during drain execution. |
| `drainPreview` | `NodeDrainPreview` | Dry-run analysis computed before drain starts. |

### Drain preview (`status.nodes[].drainPreview`)

Computed before drain starts and not updated once drain is in progress.

| Field | Type | Description |
|-------|------|-------------|
| `evictableCount` | `int32` | Pods that will be evicted with current settings. |
| `skippedCount` | `int32` | Pods that will be skipped (DaemonSets, mirror pods, completed pods). |
| `issues` | `[]NodeIssue` | Pods that will block or complicate drain. |
| `computedAt` | `Time` | When this preview was last computed. |
