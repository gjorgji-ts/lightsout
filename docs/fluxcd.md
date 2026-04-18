# FluxCD Integration

LightsOut can optionally suspend FluxCD `Kustomization` and `HelmRelease` resources during downscale windows to prevent FluxCD from reconciling scaled-down workloads back to their Git-desired state.

## The Problem

When LightsOut scales Deployments to zero replicas, FluxCD detects drift between the live cluster state and Git and reconciles the workloads back up. Unlike ArgoCD (which supports `ignoreDifferences`), FluxCD has no equivalent mechanism. The only reliable way to prevent reconciliation is to suspend Flux resources during the downscale window.

## The Solution

When `spec.fluxCD` is set on a schedule, LightsOut suspends matching `Kustomization` and `HelmRelease` resources before scaling workloads down, and resumes them once pods are ready after upscale.

LightsOut does **not** modify Source resources (GitRepository, HelmRepository). Suspension only pauses reconciliation, the source objects and Git history are untouched.

## Configuration

```yaml
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: dev-weekday-hours
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "America/New_York"
  namespaceSelector:
    matchLabels:
      environment: dev
  fluxCD:
    namespace: flux-system   # default; where Flux resources live
    warmupTimeout: 10m       # default; how long to keep Flux suspended post-upscale
```

Setting `fluxCD` to any value (even `{}`) enables the feature. Omitting it entirely disables it.

| Field | Type | Default | Description |
|---|---|---|---|
| `fluxCD` | object | `nil` (disabled) | When present, enables FluxCD integration |
| `fluxCD.namespace` | string | `flux-system` | Namespace where Kustomization/HelmRelease resources live |
| `fluxCD.warmupTimeout` | duration | `10m` | How long to keep Flux suspended after upscale before resuming regardless of pod readiness |

## RBAC

FluxCD integration requires additional cluster-wide permissions. Enable them in your Helm values:

```yaml
rbac:
  fluxcd: true
```

Without this, the controller cannot list or update Flux resources, and the feature will fail with warning events on the schedule.

## How It Works

### Discovery

LightsOut searches two locations for matching Flux resources:

1. Cluster-wide: scans all namespaces for resources whose `spec.targetNamespace` matches any of the schedule's target namespaces. This covers the standard centralised pattern (`flux-system`), multi-tenant setups where teams keep resources in their own namespaces, and any other location.
2. The target namespaces themselves: finds resources with no `spec.targetNamespace` set (covers HelmReleases deployed in the same namespace as their workloads). The `fluxCD.namespace` value (default `flux-system`) is excluded from this search, a resource there without `spec.targetNamespace` is a system resource, not a co-located deployment.

Both `Kustomization` and `HelmRelease` are searched in both locations.

> **Important - Kustomizations must set `spec.targetNamespace`:** A `Kustomization` in `flux-system` (or any namespace outside the target) is only discovered if it has `spec.targetNamespace` matching the schedule's target namespace. A Kustomization in `flux-system` without `spec.targetNamespace` is treated as a system resource and skipped, even if it deploys workloads into a target namespace. Without suspension, kustomize-controller will continue reconciling every `spec.interval` and fight back against LightsOut's scaled-down replicas.
>
> **What to do:** If your Kustomization deploys resources into a single namespace, set `spec.targetNamespace` on it. If a single Kustomization deploys to multiple namespaces (mixed-namespace resources), split it into one Kustomization per namespace, each with its own `spec.targetNamespace`. See the [example below](#kustomization-targetnamespace-pattern).
>
> **Other non-discoverable cases:** Resources that target namespaces via `spec.patches`, cross-namespace chart references, or other non-standard mechanisms are also not discovered automatically and require manual management.

### Kustomization targetNamespace Pattern

For LightsOut to discover and suspend a `Kustomization` living in `flux-system`, it must have `spec.targetNamespace` pointing at the workload namespace:

```yaml
# ✅ Discoverable - spec.targetNamespace tells lightsout which namespace this manages
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend
  namespace: flux-system
spec:
  targetNamespace: team-backend   # lightsout matches this against its target namespaces
  path: ./apps/my-app/backend
  sourceRef:
    kind: GitRepository
    name: flux-system
```

```yaml
# ❌ Not discoverable - flux-system Kustomization without targetNamespace is treated as a system resource and skipped. kustomize-controller will reconcile every spec.interval and restore replicas that lightsout has scaled to zero.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend
  namespace: flux-system
spec:
  path: ./apps/my-app/backend     # deploys to team-backend, but lightsout can't know that
  sourceRef:
    kind: GitRepository
    name: flux-system
```

If a single Kustomization deploys to multiple namespaces (e.g. it includes a `HelmRelease` in `flux-system` and `Deployment` resources in `team-backend`), setting `spec.targetNamespace` is not possible, it would override **all** resource namespaces. In this case, split into separate Kustomizations:

```yaml
# Base Kustomization: flux-system resources (HelmReleases with their own targetNamespace)
# This one is discovered via the HelmRelease's own spec.targetNamespace, no change needed.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app           # manages flux-system HelmReleases
  namespace: flux-system
spec:
  path: ./apps/my-app/base
  ...
---
# Per-namespace Kustomization: plain workloads for team-backend
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend   # manages team-backend Deployments/StatefulSets/etc.
  namespace: flux-system
spec:
  targetNamespace: team-backend
  path: ./apps/my-app/backend
  dependsOn:
    - name: my-app
  ...
```

### State Machine

LightsOut uses a three-state lifecycle on matching Flux resources:

| State | `lightsout.techsupport.mk/state` | `spec.suspend` | When |
|---|---|---|---|
| Downscaled | `down` | `true` | Workloads are at 0 replicas |
| Warming up | `warming-up` | `true` | Workloads scaling up; pods not yet ready |
| Up (normal) | _(absent)_ | `false` | All workloads healthy |

### Execution Ordering

**Downscale:**
1. Label Flux resources `state=down` + `managed-by=<schedule>`
2. Suspend Flux resources (`spec.suspend: true`)
3. Scale workloads to zero

**Upscale:**
1. Scale workloads back up
2. Transition Flux resources to `warming-up` (still suspended)
3. Poll every 30 seconds for pod readiness via `CheckWorkloadReadiness`
4. Once all pods are ready **or** `warmupTimeout` elapses: set `spec.suspend: false`, remove all lightsout labels

### User-Managed Suspension

If a Flux resource is already suspended by a user (no `lightsout.techsupport.mk/managed-by` label), LightsOut will not touch it. This preserves user intent, LightsOut only manages resources it has claimed.

## Alert Suppression

The operator adds `lightsout.techsupport.mk/state` labels to suspended Flux resources. Since suspended resources do not reconcile, most false alerts are suppressed by the suspension itself.

For external monitoring tools or FluxCD Alert resources configured to fire on label changes, you can use `spec.eventSources[].matchLabels` to exclude resources carrying lightsout labels:

```yaml
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata:
  name: flux-alerts
  namespace: flux-system
spec:
  providerRef:
    name: slack
  eventSeverity: error
  eventSources:
    - kind: Kustomization
      name: '*'
      namespace: flux-system
  exclusionList:
    - ".*lightsout.*"
```

## Multi-Schedule Safety

- A Flux resource already labelled by a different schedule is skipped.
- Only the schedule that suspended a resource can resume it.
- Operations are idempotent, suspending an already-suspended (lightsout-managed) resource is a no-op.

## Schedule Deletion

When a schedule is deleted, the finalizer cleanup resumes all Flux resources suspended by that schedule before allowing deletion. No Flux resources are left permanently suspended.

## Requirements

This integration targets the stable FluxCD APIs:

| Resource | API group | Version | Minimum Flux version |
|---|---|---|---|
| Kustomization | `kustomize.toolkit.fluxcd.io` | `v1` | Flux v2.0.0+ |
| HelmRelease | `helm.toolkit.fluxcd.io` | `v2` | Flux v2.3.0+ |

If your cluster runs Flux older than v2.3.0, HelmRelease resources use `v2beta2` or `v2beta1` and will not be discovered. LightsOut degrades gracefully, workload scaling continues, but HelmReleases are not suspended. Upgrade Flux to v2.3.0+ to enable full integration.

## Known Limitations

**In-flight reconciliation at downscale time:** FluxCD's `spec.suspend` does not interrupt a reconciliation that is already running when LightsOut sets it. Any reconciliation in progress at the moment of suspension will run to completion. Subsequent reconciliations are blocked. In practice this window is short (seconds), but workloads scaled to zero immediately after a Flux reconcile begins may be briefly scaled back up before the next reconcile attempt is blocked.

## Graceful Degradation

- **FluxCD not installed**: discovery returns empty and scaling continues normally.
- **Flux older than v2.3.0**: HelmRelease `v2` CRD not found, HelmReleases are skipped, Kustomizations still work if Flux ≥ v2.0.0.
- **Flux operation fails**: errors are logged and emitted as Kubernetes `Warning` events on the schedule, but workload scaling is never blocked.
- **`spec.fluxCD` nil**: feature is completely inactive with zero overhead.
