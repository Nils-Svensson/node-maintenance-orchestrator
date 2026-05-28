# Architecture

## Overview

`node-maintenance-orchestrator` is a Kubernetes controller built with [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime). It watches `NodeMaintenancePlan` resources and `Node` resources, and drives cordon and drain to completion through a level-driven reconciliation loop.

---

## Reconciliation loop

The controller reconciles in response to:

- Changes to a `NodeMaintenancePlan` (spec changes, status updates, deletion)
- Changes to a `Node` that is referenced by or relevant to a plan (cordon state changes, annotation changes)

Each reconcile pass runs these steps in order:

1. **ComputeOwnershipResolution** — resolves which nodes the plan should own, which it already owns, and which conflict with other plans. Handles `nodes` (explicit list) and `nodeSelector` (snapshotted label selector).

2. **ReconcileOwnership** — adopts new nodes (adds `maintenance.nmoo.io/managed-by` annotation), releases nodes removed from the spec, and cordons nodes if cordon is enabled and the schedule has fired.

3. **UpdateStatus** — writes per-node status (cordoned, drifted, drift reason), sets plan-level conditions (NodesSelected, Cordoned, Conflict, Scheduled, DriftDetected), and sets `observedGeneration`. Must run before `ReconcileDrift` because drift detection requires the managed-by annotation to still be present.

4. **ReconcilePreview** — computes a dry-run analysis of what drain will do to each node. Classifies pods as evictable, skipped, or potentially blocking. Runs before drain starts and is not updated once `DrainInProgress` is true.

5. **ReconcileDrift** — handles nodes that have diverged from desired state. Releases ownership of manually uncordoned nodes; fires events for externally cordoned nodes. See [Drift handling](drift-handling.md).

6. **ReconcileCordon** — applies or removes the cordon on stable nodes based on `cordon.enabled` and whether the schedule has fired.

7. **ReconcileDrain** — drives pod eviction on cordoned nodes, up to `maxParallel` at a time. Updates drain counters and conditions. Returns a requeue duration so the reconciler re-checks without waiting for an external event.

---

## Ownership model

The operator tracks ownership through a node annotation:

```
maintenance.nmoo.io/managed-by: <plan-name>
```

This annotation is added when a node is adopted and removed when it is released (drift, spec removal, or plan deletion). Two plans cannot own the same node — the second plan to reconcile detects the annotation and marks the node as `Conflicting`.

The operator is **cooperative**: it does not re-enforce cordon state against external changes. If a node is manually uncordoned, the operator releases ownership rather than re-cordoning. See [Drift handling](drift-handling.md).

---

## Node selection and snapshots

**`spec.nodes`** — explicit list, evaluated on every reconcile. Adding or removing names takes effect immediately.

**`spec.nodeSelector`** — label selector, snapshotted on the first reconcile. `status.resolvedNodes` records the snapshot and `status.nodeSnapshotTaken` is set to `true`. Subsequent reconciles use the snapshot, not a live label query. This prevents nodes added to the cluster after plan creation from being automatically adopted into an ongoing maintenance window.

---

## Drain model

Drain is **level-driven**, not event-driven. Each reconcile pass:

1. Lists all pods on cordoned nodes.
2. Classifies them: evictable, blocked (PDB or config), or terminating.
3. Sends eviction requests for evictable pods and returns immediately — it does not wait for pods to terminate.
4. The requeue timer fires again after a short interval to check termination progress.

This avoids blocking the reconcile goroutine on pod termination, which can take tens of seconds or more per pod.

Drain slots (`maxParallel`) are tracked by counting how many nodes currently have evictions in progress. A slot opens the moment a node finishes draining — not when it is uncordoned by the operator.

---

## Scheduling

`cordon.startAt` and `drain.startAt` are evaluated on each reconcile against the current wall clock. If the schedule has not yet fired, the controller requeues at the scheduled time. Once fired, the time is not re-checked — the action is idempotent.

---

## Controller setup

The controller is registered with the manager as a `NodeMaintenancePlan` controller. It also watches `Node` resources, mapping each node change to the set of plans that reference it in their spec. This avoids watching and re-reconciling all plans on every node change.

Up to 10 plans are reconciled concurrently (`MaxConcurrentReconciles: 10`). Plans for different node sets are independent.
