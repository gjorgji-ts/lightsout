# HPA Integration

LightsOut automatically handles HorizontalPodAutoscalers (HPAs) attached to Deployments and StatefulSets. When an HPA is present, LightsOut sets `spec.behavior.scaleUp.selectPolicy: Disabled` before scaling the workload down, then restores it after scaling back up. No configuration is required.

## The Problem

When LightsOut scales a Deployment or StatefulSet to zero replicas, an attached HPA with `spec.minReplicas >= 1` will immediately fight back: the HPA controller sees `replicas < minReplicas` and corrects it, silently defeating the off-hours scale-down.

This is a pure Kubernetes control-plane conflict. The reliable fix is to disable the HPA's scale-up behaviour before zeroing the workload, then restore it on upscale.

## How It Works

The feature is automatic and requires no API changes or schedule configuration. LightsOut handles it transparently for any Deployment or StatefulSet that has an attached HPA.

**On downscale:**
1. LightsOut finds the HPA targeting the workload (by `spec.scaleTargetRef`).
2. Stores the original `spec.behavior.scaleUp.selectPolicy` value in an annotation on the HPA.
3. Sets `spec.behavior.scaleUp.selectPolicy: Disabled` to prevent the HPA from fighting back.
4. Scales the workload's `spec.replicas` to `0`.

Disabling scale-up first prevents the HPA from observing `replicas < minReplicas` even during the brief window between the two API writes. The HPA's `spec.minReplicas` and metric configuration are **not modified**.

**On upscale:**
1. Restores the workload's `spec.replicas` to the original count.
2. Restores the HPA's `spec.behavior.scaleUp.selectPolicy` from the stored annotation.
3. Removes all LightsOut annotations from the HPA.

Restoring replicas first means the HPA sees the workload already at its target count when scale-up is re-enabled — no redundant reconcile is triggered.

## Annotations

LightsOut uses these annotations on the HPA object during the downscale window:

| Annotation | Description |
|---|---|
| `lightsout.techsupport.mk/original-hpa-scale-up-policy` | The original `spec.behavior.scaleUp.selectPolicy` value (empty string = field was absent) |
| `lightsout.techsupport.mk/managed-by` | The schedule name that patched this HPA |

Both annotations are removed when the HPA is restored.

## Skip Conditions

LightsOut will not patch an HPA if any of the following are true:

- **No HPA found** - workloads without an HPA are unaffected; no error is reported.
- **User-managed disabled scale-up** - if the HPA already has `spec.behavior.scaleUp.selectPolicy: Disabled` and no `managed-by` annotation, LightsOut treats it as intentionally configured and does not claim ownership.
- **Owned by a different schedule** - multi-schedule safety: only the schedule that patched an HPA can restore it.
- **Already patched** - if LightsOut previously patched the HPA, repeated reconciles are idempotent.

## Multi-Schedule Safety

LightsOut's multi-schedule safety rules apply to HPAs the same way they apply to workloads:

- An HPA already annotated with `managed-by` pointing to a different schedule is skipped on both patch and restore.
- Only the owning schedule may restore the HPA.

## RBAC

HPA permissions are always granted. No configuration is required.

| Resource | API Group | Verbs |
|---|---|---|
| `horizontalpodautoscalers` | `autoscaling` | get, list, watch, update, patch |

## CronJobs

CronJobs are excluded from HPA handling. Kubernetes does not support HPAs targeting CronJobs (CronJobs do not expose the `scale` subresource), so no HPA logic is applied when suspending or resuming CronJobs.

## Graceful Degradation

- **Cluster without `autoscaling/v2`** - LightsOut detects the missing API and skips HPA handling silently. Workload scaling proceeds normally.
- **HPA operation fails** - errors are logged at error level but do not block workload scaling. If patching fails on downscale, the workload still scales to zero but the HPA may fight back until the next reconcile corrects it.

## Limitations

- **LightsOut uses `autoscaling/v2` for HPA discovery** - HPAs created via `autoscaling/v1` are served across both API versions by the Kubernetes API server, so they are discovered and patched correctly.
- **`spec.minReplicas` and metric targets are not modified** - LightsOut only touches `spec.behavior.scaleUp.selectPolicy`. HPA scaling behaviour and minimum replica counts during business hours are unchanged.
- **If a user-configured `spec.behavior.scaleUp` has other settings** (policies, `stabilizationWindowSeconds`) they are preserved. LightsOut only sets/restores the `selectPolicy` sub-field.
