# Maintenance Lifecycle

## Phases

A `NodeMaintenancePlan` moves through the following phases, reported in `status.phase`:

| Phase | Meaning |
|-------|---------|
| `Pending` | Plan created, no nodes adopted yet. |
| `Adopted` | At least one node is under management. |
| `Scheduled` | Nodes adopted but `cordon.startAt` is still in the future. |
| `Cordoned` | All managed nodes are unschedulable. |
| `Draining` | Pod evictions are in flight or pods are terminating. |
| `Blocked` | All nodes have blocking pods and no evictions are making progress. |
| `Ready` | All managed nodes have `readyForMaintenance=true`. Physical maintenance can begin. |
| `TimedOut` | Drain did not complete within `drain.timeoutMinutes`. |
| `Conflict` | One or more nodes are already owned by another plan. |

Phases are derived from conditions on each reconcile. `Blocked` and `Draining` can coexist — if some nodes are draining and others are blocked, the phase is `Draining`.

---

## Full workflow

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
  reason: "kernel upgrade"
  cordon:
    enabled: true
    startAt: "2026-06-01T02:00:00Z"
  drain:
    enabled: true
    startAt: "2026-06-01T02:15:00Z"
    timeoutMinutes: 60
    options:
      maxParallel: 2
```

The operator immediately adopts all three nodes, adds the `maintenance.nmoo.io/managed-by` annotation to each, and computes a **drain preview** — a dry-run analysis of what drain will do to each node (see [Drain preview](#drain-preview)). Phase moves to `Adopted` or `Scheduled` depending on how far away `cordon.startAt` is.

### 2. Cordon fires

At `cordon.startAt`, the operator marks all nodes unschedulable and the phase transitions to `Cordoned`. No pods are evicted yet.

The 15-minute gap between `cordon.startAt` and `drain.startAt` in this example gives load balancers time to remove the nodes from their backend pools before pods start terminating.

### 3. Drain begins

At `drain.startAt`, drain starts on up to `maxParallel` nodes at a time. Phase moves to `Draining`.

If `drain.startAt` is omitted, drain begins as soon as nodes are cordoned — there is no delay between cordon and drain. Set `drain.startAt` only when you want a deliberate gap, for example to allow load balancers to shed connections before pods start terminating.

Each drain pass:
1. Classifies pods as evictable, blocked (PDB or config), or terminating.
2. Sends eviction requests for evictable pods.
3. Requeues after a short interval to check termination.

As each node finishes draining, the drain slot it held is freed immediately — the next node starts without waiting for the others.

### 4. Monitor progress

```bash
kubectl get nmp worker-upgrade
```

```text
NAME             PHASE      READY   PROGRESS   DRAINING   BLOCKED   DRIFT   AGE
worker-upgrade   Draining   0/3     33%        2/3        0/3       false   18m
```

For per-node detail, events, and blocked pod information:

```bash
kubectl describe nmp worker-upgrade
```

### 5. Perform maintenance

When a node reaches `readyForMaintenance=true`, the plan phase eventually reaches `Ready` once all nodes complete. At this point you can perform the physical maintenance (reboot, firmware update, OS upgrade).

The cordon persists across reboots — it is stored in the node spec in etcd, not in memory on the node. The node comes back unschedulable after a reboot and requires an explicit uncordon.

### 6. Return nodes to service

**Option A — return nodes individually as they complete:**

```bash
kubectl uncordon worker-1
```

The operator detects the uncordon, sees that `readyForMaintenance=true` for `worker-1`, fires a `MaintenanceComplete` event, and releases ownership (annotation removed). This is not recorded as drift. Repeat for each node as its maintenance finishes.

**Option B — return all nodes at once:**

Skip individual uncordons and go straight to deleting the plan (step 7). The finalizer uncordons all remaining nodes in one pass.

### 7. Delete the plan

```bash
kubectl delete nmp worker-upgrade
```

The finalizer runs before the object is removed. Every node the plan still owns is uncordoned and its `maintenance.nmoo.io/managed-by` annotation is removed. If all nodes were already released individually, the finalizer is a no-op and deletion is immediate.

---

## Drain timeout

If `drain.timeoutMinutes` is set and drain does not complete in time, the plan moves to `TimedOut`. The operator stops retrying. `DrainTimedOut=True` is set as a condition and a `DrainTimedOut` warning event is fired.

The plan object is not deleted automatically. You must investigate the blocking pods and delete the plan manually when ready.

---

## Removing a node mid-plan

If you remove a node name from `spec.nodes`, the operator detects the change on the next reconcile and releases that node: it is uncordoned and its annotations are removed. The remaining nodes in the plan continue unaffected.

---

## Conflict between plans

Two plans cannot manage the same node. If a node in `spec.nodes` is already owned by another plan, the plan enters the `Conflict` phase and an `OwnershipConflict` warning event is fired. The conflicting node is not managed by either plan until the conflict is resolved.

---

## Drain preview

Before drain begins, the operator computes a dry-run analysis for each managed node and stores it in `status.nodes[].drainPreview`. The preview uses the same pod classification logic as drain itself, so the results reflect exactly what drain will do with the configured options.

Each preview includes:
- **`evictableCount`** — pods that will be evicted.
- **`skippedCount`** — pods that will be skipped (DaemonSets, mirror pods, completed pods).
- **`issues`** — pods that will block or complicate drain, with the reason (PDB, emptyDir, no controller, etc.).
- **`computedAt`** — when the preview was last computed.

Plan-level summaries are also set: `status.totalEvictablePods` and `status.totalPreviewIssues`.

The preview is recomputed on each reconcile until `DrainInProgress` becomes true, at which point it is frozen. This means previews are kept up to date if pod workloads change before drain starts, but are not updated mid-drain.

Preview results are visible in `kubectl describe nmp <name>` and are useful for spotting drain blockers before committing to a maintenance window.
