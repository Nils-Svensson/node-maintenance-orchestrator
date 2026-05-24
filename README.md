# node-maintenance-orchestrator

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
| `DrainInProgress` | Evictions are in flight |
| `DrainSucceeded` | All managed nodes are empty |
| `DrainBlocked` | One or more pods cannot be evicted |
| `DrainTimedOut` | Drain did not complete within `timeoutMinutes` |
| `ConflictDetected` | A node is already owned by another plan |
| `DriftDetected` | A managed node was modified outside this plan |

## Prerequisites

- Kubernetes v1.30.0+
- kubectl v1.30.0+

## Contributing

Issues and pull requests are welcome. Please open an issue before starting significant work so we can discuss the approach.

## License

Copyright 2026. Licensed under the [Apache License, Version 2.0](LICENSE).
