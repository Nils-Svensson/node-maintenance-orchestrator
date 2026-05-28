# Drift Handling

Drift occurs when a managed node's cordon state diverges from what the plan expects. The operator is intentionally **cooperative**: it does not fight external changes. When drift is detected, the operator records it, fires an event, and in most cases releases ownership rather than re-enforcing the desired state.

---

## Drift types

### ManualUncordon

**Trigger:** A node managed by a plan with `cordon.enabled: true` is manually uncordoned while still under management and not yet `readyForMaintenance`.

**What the operator does:**
1. Detects that `node.spec.unschedulable=false` but the operator's cordon annotation is present.
2. Fires a `DriftDetected` warning event.
3. **Releases ownership** — removes the `maintenance.nmoo.io/managed-by` annotation. Does not re-cordon.
4. Marks the node as `drifted=true` in status until it is removed from the plan spec.

**Recovery:** Remove the node from `spec.nodes` and re-add it. The operator will re-adopt and re-cordon on the next reconcile. For `nodeSelector`-based plans, delete and recreate the plan.

---

### ExternalCordon

**Trigger:** A node managed by a plan with `cordon.enabled: false` becomes unschedulable due to an external actor (cluster autoscaler, another operator, manual `kubectl cordon`).

**What the operator does:**
1. Detects that `node.spec.unschedulable=true` but the operator did not apply the cordon.
2. Fires a `DriftDetected` warning event.
3. **Retains ownership** — does not uncordon. Does not interfere with the external cordon.
4. Marks the node as `drifted=true` in status.

The operator assumes the external actor has a reason for the cordon. When the external actor uncordons the node, drift clears automatically on the next reconcile.

---

### MaintenanceComplete

**Trigger:** A node is uncordoned after its `readyForMaintenance` flag is already `true`. This is the expected path for returning a node to service after maintenance.

**What the operator does:**
1. Detects the uncordon.
2. Fires a `MaintenanceComplete` **normal** event (not a drift warning).
3. Releases ownership cleanly.

This is not recorded as drift in status. The transition is treated as an expected lifecycle event, not an anomaly.

---

## Drift in status

When a node is drifted:

- `status.nodes[].drifted=true`
- `status.nodes[].driftReason` is set to `ManualUncordon` or `ExternalCordon`
- `status.drifted=true` (plan-level aggregate)
- `DriftDetected` condition is set to `True` with the names of drifted nodes in the message

Drifted nodes are excluded from the `Cordoned` phase check. A plan where all non-drifted nodes are cordoned reaches the `Cordoned` phase even if some drifted nodes are unschedulable.

Drift state is carried forward in status until the node is removed from the plan spec. If the operator's annotation is removed (as part of drift handling), the drift is preserved from the previous status entry rather than re-detected from annotations.

---

## External cordon before scheduled cordon fires

If a node is externally cordoned while the plan has `cordon.enabled=true` but `cordon.startAt` is still in the future (so the operator has not yet applied its own cordon), this situation is **not currently detected as drift**. Neither ManualUncordon (which requires the operator's own cordon annotation to be present) nor ExternalCordon (which requires `cordon.enabled=false`) conditions are met.

In practice this is mostly benign: when `cordon.startAt` arrives, the operator adds its cordon annotation to the already-unschedulable node and proceeds normally. The end state is identical to the operator having cordoned it. The one consequence is that the node stops accepting new workloads earlier than the planned maintenance window, which may be unexpected for workloads that were scheduled with the assumption the node would remain available until `cordon.startAt`.

This is a known gap and may be addressed in a future release.

---

## Drift and the `DriftDetected` condition

The `DriftDetected` condition on the plan reflects the current drift state:

```text
Type:    DriftDetected
Status:  True
Reason:  NodeDrifted
Message: 2 node(s) drifted: worker-1, worker-3
```

Unlike events, conditions persist and are visible on every `kubectl describe`. Use them for alerting.
