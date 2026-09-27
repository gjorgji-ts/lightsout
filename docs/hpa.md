# HPA integration

LightsOut handles HorizontalPodAutoscalers attached to Deployments and StatefulSets. Before it scales a workload down, it sets `spec.behavior.scaleUp.selectPolicy: Disabled` on the HPA. It restores the original value on upscale. This needs no configuration.

## The problem

An HPA with `spec.minReplicas >= 1` undoes the downscale. The HPA controller sees `replicas < minReplicas` and corrects it, so the workload returns within seconds and the schedule reports success.

This is a conflict between two controllers, not a bug in either. The answer is to disable the HPA scale-up behaviour before the replica write, and restore it on upscale.

## How it works

On downscale, LightsOut:

1. Finds the HPA that targets the workload, through `spec.scaleTargetRef`.
2. Stores the original `spec.behavior.scaleUp.selectPolicy` in an annotation on the HPA.
3. Sets `spec.behavior.scaleUp.selectPolicy: Disabled`.
4. Sets the workload `spec.replicas` to `0`.

The policy write comes first on purpose. It closes the window between the two API calls, where the HPA would otherwise see `replicas < minReplicas` and react.

On upscale, LightsOut:

1. Restores the workload `spec.replicas` to the original count.
2. Restores `spec.behavior.scaleUp.selectPolicy` from the annotation.
3. Removes its own annotations from the HPA.

The replica write comes first here, for the mirror-image reason. The HPA sees the workload already at its target count when scale-up returns, so it has nothing to correct.

## Annotations

LightsOut writes these annotations on the HPA for the length of the downscale:

| Annotation | Description |
|---|---|
| `lightsout.techsupport.mk/original-hpa-scale-up-policy` | The original `spec.behavior.scaleUp.selectPolicy`. An empty string records that the field was absent. |
| `lightsout.techsupport.mk/managed-by` | The schedule that patched this HPA |

The upscale removes both.

## Skip conditions

LightsOut leaves an HPA alone in these cases:

- **No HPA found.** A workload without an HPA is unaffected, and this reports no error.
- **Scale-up already disabled by a user.** An HPA that carries `selectPolicy: Disabled` and no `managed-by` annotation is deliberately configured that way, so LightsOut does not claim it.
- **Owned by a different schedule.** Only the schedule that patched an HPA can restore it.
- **Already patched.** Repeated reconciles are idempotent.

## Multi-schedule safety

The rules that protect workloads protect HPAs the same way. An HPA whose `managed-by` annotation names a different schedule is skipped on both the patch and the restore. Only the owning schedule restores it.

## RBAC

HPA permissions are always granted, and need no configuration.

| Resource | API group | Verbs |
|---|---|---|
| `horizontalpodautoscalers` | `autoscaling` | get, list, watch, update, patch |

## CronJobs

Kubernetes does not support an HPA that targets a CronJob, because a CronJob exposes no `scale` subresource. LightsOut therefore runs no HPA logic when it suspends or resumes one.

## Graceful degradation

- **A cluster without `autoscaling/v2`.** LightsOut detects the missing API and skips HPA handling. Workload scaling continues.
- **An HPA operation fails.** LightsOut logs the error and continues, so a failure here never blocks workload scaling. The workload still reaches zero, but the HPA can undo it until a later reconcile succeeds.

## Limitations

- LightsOut discovers HPAs through `autoscaling/v2`. The API server serves an HPA created through `autoscaling/v1` on both versions, so those are found and patched correctly.
- LightsOut does not modify `spec.minReplicas` or the metric targets. It touches `spec.behavior.scaleUp.selectPolicy` and nothing else, so HPA behaviour during business hours is unchanged.
- A user-configured `spec.behavior.scaleUp` keeps its other settings, such as `policies` and `stabilizationWindowSeconds`. LightsOut reads and writes the `selectPolicy` field alone.
