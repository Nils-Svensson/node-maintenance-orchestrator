# Node-Maintenance-Orchestrator

A Kubernetes operator for automating node maintenance preparation. Declare which nodes need maintenance and when — the operator handles cordoning, draining, PDB-aware eviction, and progress tracking.

## Overview

Before taking a node offline for maintenance (hardware repairs, OS upgrades, kernel patches), you need to cordon it to block new scheduling and drain it by evicting all existing pods. `kubectl drain` works for one-off operations, but becomes cumbersome when:

- Coordinating maintenance across multiple nodes with a controlled rollout
- Respecting PodDisruptionBudgets across a fleet without manual intervention
- Scheduling maintenance windows in advance
- Tracking which nodes are ready and which are blocked

`node-maintenance-orchestrator` lets you express this as a `NodeMaintenancePlan` resource. The operator drives cordon and drain to completion, reports per-node progress and blockers, and marks each node ready for maintenance once its workloads are gone.

### Key features

- **Scheduled maintenance windows** — set when cordon and drain begin; the operator handles timing and retries
- **Multi-node rollout control** — drain nodes one at a time or in parallel, with configurable concurrency
- **PDB-aware eviction** — respects PodDisruptionBudgets by default; configurable bypass per plan
- **Pre-drain preview** — dry-run analysis surfaces blocked pods and potential issues before evictions begin
- **Per-node progress tracking** — drain progress percentage, blocked pod details, and status conditions per node
- **Drain timeout** — declare a deadline; the plan stops retrying and is marked `DrainTimedOut` if drain does not complete in time
- **Point-in-time node selection** — label-based plans snapshot the matching node set at creation; new nodes matching the selector later are not automatically adopted
- **Safe concurrent plans** — two plans cannot manage the same node; conflicts are detected and reported

## Install

### Helm (recommended)

```sh
helm install nmo oci://ghcr.io/nils-svensson/charts/node-maintenance-orchestrator \
  --version <version> \
  --namespace nmo-system \
  --create-namespace
```

To list available versions:

```sh
helm search repo oci://ghcr.io/nils-svensson/charts/node-maintenance-orchestrator
```

### kubectl

Apply the single-file manifest from a release:

```sh
kubectl apply -f https://github.com/Nils-Svensson/node-maintenance-orchestrator/releases/download/<version>/install.yaml
```

### Kustomize

Reference the `config/default` base directly in your own `kustomization.yaml`:

```yaml
resources:
  - https://github.com/Nils-Svensson/node-maintenance-orchestrator/config/default?ref=<version>

images:
  - name: controller
    newName: ghcr.io/nils-svensson/node-maintenance-orchestrator
    newTag: <version>
```

## Quick start

### Cordon and drain two nodes at a scheduled time

```yaml
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: worker-maintenance
spec:
  nodes:
    - worker-1
    - worker-2
  reason: "scheduled kernel upgrade"
  cordon:
    enabled: true
    startAt: "2026-06-01T02:00:00Z"
  drain:
    enabled: true
    timeoutMinutes: 30
    options:
      podTerminationGracePeriodSeconds: 30
```

When `drain.startAt` is omitted, drain begins as soon as the nodes are cordoned — there is no separate drain delay. Set `drain.startAt` only if you want a deliberate gap between cordon and drain (for example, to let load balancers shed connections before pods are evicted).

Check progress:

```sh
kubectl get nmp worker-maintenance
kubectl describe nmp worker-maintenance
```

### Select nodes by label

```yaml
spec:
  nodeSelector:
    matchLabels:
      node-role: compute
  cordon:
    enabled: true
  drain:
    enabled: true
```

Nodes matching the selector are snapshotted when the plan is created. Nodes added to the cluster afterward — even if they match the selector — are not adopted.

## Workflow walkthrough

A complete maintenance cycle for three nodes: all cordoned at the same time, drained two at a time, returned to service individually as each completes.

### 1. Create the plan

```yaml
apiVersion: maintenance.nmoo.io/v1alpha1
kind: NodeMaintenancePlan
metadata:
  name: worker-upgrade
spec:
  nodes:
    - worker-1
    - worker-2
    - worker-3
  reason: "kernel 6.8 upgrade"
  cordon:
    enabled: true
    startAt: "2026-06-01T02:00:00Z"
  drain:
    enabled: true
    startAt: "2026-06-01T02:15:00Z"
    timeoutMinutes: 60
    options:
      maxParallel: 2
      podTerminationGracePeriodSeconds: 60
```

The operator immediately adopts all three nodes. Cordon fires at 02:00 — the 15-minute gap gives load balancers time to shed connections before pods are evicted. Drain starts at 02:15, processing two nodes at a time.

### 2. Monitor progress

```sh
kubectl get nmp worker-upgrade -w
```

At 02:00 all three nodes are cordoned (`Cordoned=True`). At 02:15 drain begins on worker-1 and worker-2 in parallel (`DrainInProgress=True`). For per-node detail — drain progress, blocked pods, warning events:

```sh
kubectl describe nmp worker-upgrade
```

### 3. Return nodes to service as they complete

Nodes finish draining at different times depending on their workload. As soon as worker-1 finishes draining and reaches `ReadyForMaintenance=true`, the drain slot it held is freed and drain starts on worker-3 immediately — no uncordon required.

When a node reaches `ReadyForMaintenance=true`, perform the physical maintenance (reboot, firmware update, etc.), then uncordon it:

```sh
kubectl uncordon worker-1
```

The operator detects the uncordon, sees that `ReadyForMaintenance=true` for worker-1, emits a `MaintenanceComplete` Normal event, and releases ownership — removing the `managed-by` annotation. No `DriftDetected` warning is raised.

Repeat for each node as it completes. The cordon persists across reboots because it is stored in the node spec in etcd, not in memory on the node — so the node comes back unschedulable after a reboot and needs an explicit uncordon.

### 4. Delete the plan

Once you have returned all nodes to service:

```sh
kubectl delete nmp worker-upgrade
```

The finalizer runs and releases any remaining owned nodes (annotations removed, cordons lifted). If all nodes were already released individually in step 3, the finalizer is a no-op and deletion is immediate.

> For a single-node plan there is no need to uncordon manually first — just delete the plan and the finalizer handles everything.

## Lifecycle and behavior

### Plan deletion

When a `NodeMaintenancePlan` is deleted, the operator's finalizer runs before the object is removed. Every node the plan owns is uncordoned (if the operator applied the cordon) and the `maintenance.nmoo.io/managed-by` annotation is removed. The cluster is left in a clean state regardless of how far through the drain process the plan had progressed.

### Removing a node from the plan spec

If you remove a node name from `spec.nodes`, the operator detects the change on the next reconcile and releases that node: it is uncordoned and its annotations are removed. The remaining nodes in the plan are unaffected.

### Manual uncordon

If you manually `kubectl uncordon` a node that the operator cordoned, the operator detects the mismatch as drift and **releases ownership of that node**. It will not re-cordon it. A `DriftDetected` warning event is fired on the plan. The operator is intentionally cooperative here - it assumes the external actor has a reason for uncordoning and does not fight it. To resume management of that node, remove it from the plan spec and re-add it. In the case of using nodeSelector, delete and re-create plan to clear drift.  

### External cordon

If a node managed by a plan with `cordon.enabled: false` becomes unschedulable due to an external actor (a cluster autoscaler, another operator, or a manual `kubectl cordon`), the operator records this as drift but **retains ownership and does not interfere**. A `DriftDetected` event is fired. The operator is intentionally cooperative here — it assumes the external actor has a reason for the cordon and does not fight it.

### Valid plan configurations

| `cordon.enabled` | `drain.enabled` | Valid | Behavior |
|-----------------|----------------|-------|----------|
| `false` | `false` | ✓ | Nodes are adopted and tracked but no action is taken. Useful for observing the plan before enabling maintenance actions. |
| `true` | `false` | ✓ | Nodes are cordoned only. Useful when you want to block new scheduling but manage pod eviction yourself. |
| `true` | `true` | ✓ | Full maintenance preparation: nodes are cordoned, then drained. |
| `false` | `true` | ✗ | Rejected at admission — drain requires cordon to be enabled. |

## Configuration reference

### `spec.nodes` / `spec.nodeSelector`

Mutually exclusive. `nodes` is an explicit list of node names; `nodeSelector` is a label selector. When using `nodeSelector`, the resolved node set is frozen on the first reconcile.

### `spec.cordon`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Cordon the selected nodes |
| `startAt` | — | RFC 3339 time at which cordon begins. Omit to cordon immediately. |

### `spec.drain`

Drain requires `cordon.enabled: true`.

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Drain the selected nodes after cordoning |
| `startAt` | — | RFC 3339 time at which drain begins |
| `timeoutMinutes` | none (infinite) | Mark the plan `DrainTimedOut` if drain does not complete within this many minutes |

### `spec.drain.options`

All fields are optional. The defaults produce behavior equivalent to `kubectl drain --ignore-daemonsets`: DaemonSet pods are skipped, PodDisruptionBudgets are respected, and pods without a controller or with emptyDir volumes block drain unless explicitly allowed.

| Field | Default | Description |
|-------|---------|-------------|
| `maxParallel` | `1` | Number of nodes drained concurrently |
| `ignoreDaemonSets` | `true` | Skip DaemonSet-managed pods instead of blocking |
| `deleteEmptyDirData` | `false` | Allow eviction of pods with emptyDir volumes (data will be lost) |
| `force` | `false` | Allow eviction of pods with no owning controller (they will not be rescheduled) |
| `podTerminationGracePeriodSeconds` | — | Override each pod's own `terminationGracePeriodSeconds`. Omit to use each pod's configured value. Set to `0` to force-kill immediately. |
| `respectPodDisruptionBudgets` | `true` | When `false`, pods blocked by a PDB are force-deleted via the Delete API instead of the Eviction API |

### Status conditions

| Condition | Meaning |
|-----------|---------|
| `NodesSelected` | At least one node has been adopted |
| `Cordoned` | All managed nodes are cordoned |
| `DrainInProgress` | Evictions are in flight or pods are terminating |
| `DrainSucceeded` | All managed nodes are empty — pods evicted and physically removed |
| `DrainBlocked` | One or more pods cannot be evicted |
| `DrainTimedOut` | Drain did not complete within `timeoutMinutes` |
| `ConflictDetected` | A node is already owned by another plan |
| `DriftDetected` | A managed node was modified outside this plan |

### Events

Events are emitted on the `NodeMaintenancePlan` object and visible via `kubectl describe nmp <name>`.

| Reason | Type | Description |
|--------|------|-------------|
| `NodeAdopted` | Normal | Node brought under management by this plan |
| `NodeCordoned` | Normal | Node marked unschedulable |
| `NodeUncordoned` | Normal | Node returned to schedulable (cordon disabled or plan deleted) |
| `NodeReleased` | Normal | Node removed from management; annotations cleared |
| `NodeDrained` | Normal | Node has no remaining evictable pods |
| `NodeReadyForMaintenance` | Normal | Node is cordoned and fully drained |
| `AllNodesReadyForMaintenance` | Normal | All nodes in the plan are ready for maintenance |
| `OwnershipConflict` | Warning | Node is already managed by another plan; this plan will not manage it |
| `DriftDetected` | Warning | Node state diverged from the plan (see [Manual uncordon](#manual-uncordon) and [External cordon](#external-cordon)) |
| `DrainBlocked` | Warning | Pod eviction rejected by a PodDisruptionBudget or blocked by plan configuration |
| `DrainFailed` | Warning | Unexpected error during eviction |
| `DrainTimedOut` | Warning | Drain deadline exceeded |

## Prerequisites

- Kubernetes v1.29.0+ (CEL validation rules in the CRD require 1.25+ beta / 1.29+ stable)
- kubectl v1.29.0+

> The operator is tested against Kubernetes v1.35+. It likely works on v1.25+ but this is not verified.

## Contributing

Issues and pull requests are welcome. Please open an issue before starting significant work so we can discuss the approach.

## License

Copyright 2026. Licensed under the [Apache License, Version 2.0](LICENSE).
