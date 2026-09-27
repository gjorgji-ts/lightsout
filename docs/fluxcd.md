# FluxCD integration

LightsOut can suspend FluxCD `Kustomization` and `HelmRelease` resources for the length of a downscale window. Flux then leaves the scaled-down workloads alone.

## The problem

Flux compares the live cluster against Git, sees a Deployment at zero replicas, and restores it. ArgoCD has `ignoreDifferences` for this. Flux has no equivalent, so the only reliable answer is to suspend the Flux resource for the window.

## The solution

When a schedule carries `spec.fluxCD`, LightsOut suspends the matching `Kustomization` and `HelmRelease` resources before it scales the workloads down. It resumes them once the pods report ready after the upscale.

LightsOut does not touch Source resources such as `GitRepository` and `HelmRepository`. Suspension pauses reconciliation and nothing else. The source objects and the Git history stay as they are.

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
    namespace: flux-system   # where the Flux resources live
    warmupTimeout: 10m       # cap on the wait for pod readiness
```

Any value enables the feature, including `{}`. Omitting the field disables it.

| Field | Type | Default | Description |
|---|---|---|---|
| `fluxCD` | object | `nil` (disabled) | Enables the integration when present |
| `fluxCD.namespace` | string | `flux-system` | Namespace that holds the Kustomization and HelmRelease resources |
| `fluxCD.warmupTimeout` | duration | `10m` | How long to keep Flux suspended after an upscale before resuming, whatever the pod state |

## RBAC

The integration needs cluster-wide permissions that you opt in to:

```yaml
rbac:
  fluxcd: true
```

Without them the controller cannot list or update Flux resources. The feature then fails and reports warning events on the schedule.

## How it works

### Discovery

LightsOut looks in two places:

1. **Every namespace**, for resources whose `spec.targetNamespace` matches one of the schedule's target namespaces. This covers the centralised `flux-system` layout, and a multi-tenant layout where each team keeps its own resources.
2. **The target namespaces**, for resources with no `spec.targetNamespace`. This covers a HelmRelease that lives beside the workloads it deploys. The search excludes `fluxCD.namespace`, because a resource there without a target namespace is a system resource rather than a co-located deployment.

Both searches cover `Kustomization` and `HelmRelease`.

> [!IMPORTANT]
> A `Kustomization` outside the target namespace is discovered only if its
> `spec.targetNamespace` names one of the schedule's namespaces. Without that
> field, LightsOut reads it as a system resource and skips it, even when it
> deploys workloads into a target namespace. kustomize-controller then keeps
> reconciling on every `spec.interval` and restores the replicas.

Set `spec.targetNamespace` on any Kustomization that deploys into one namespace. Split a Kustomization that deploys into several, one per namespace. For a worked example, see [Kustomization targetNamespace pattern](#kustomization-targetnamespace-pattern).

Discovery also misses resources that reach a namespace through `spec.patches`, through a cross-namespace chart reference, or through any other non-standard route. Manage those by hand.

### Kustomization targetNamespace pattern

A `Kustomization` in `flux-system` needs `spec.targetNamespace` to point at the workload namespace:

```yaml
# Discovered: spec.targetNamespace names the namespace this manages.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend
  namespace: flux-system
spec:
  targetNamespace: team-backend
  path: ./apps/my-app/backend
  sourceRef:
    kind: GitRepository
    name: flux-system
```

```yaml
# Not discovered: no targetNamespace, so LightsOut reads this as a system
# resource. kustomize-controller restores the replicas on every interval.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend
  namespace: flux-system
spec:
  path: ./apps/my-app/backend
  sourceRef:
    kind: GitRepository
    name: flux-system
```

One Kustomization that deploys into several namespaces cannot use the field, because `spec.targetNamespace` overrides the namespace of every resource it applies. Split it instead:

```yaml
# Base: the flux-system resources. HelmReleases carry their own
# targetNamespace, so this one needs no change.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app
  namespace: flux-system
spec:
  path: ./apps/my-app/base
  ...
---
# Per namespace: the plain workloads for team-backend.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-backend
  namespace: flux-system
spec:
  targetNamespace: team-backend
  path: ./apps/my-app/backend
  dependsOn:
    - name: my-app
  ...
```

### States

A managed Flux resource moves through three states:

| State | `lightsout.techsupport.mk/state` | `spec.suspend` | When |
|---|---|---|---|
| Downscaled | `down` | `true` | The workloads sit at zero replicas |
| Warming up | `warming-up` | `true` | The workloads are back, but the pods are not ready |
| Up | _(absent)_ | `false` | Every workload is healthy |

### Ordering

On downscale:

1. Label the Flux resources `state=down` and `managed-by=<schedule>`.
2. Suspend them.
3. Scale the workloads to zero.

On upscale:

1. Scale the workloads back up.
2. Move the Flux resources to `warming-up`, still suspended.
3. Check pod readiness every 30 seconds.
4. Once every pod is ready, or `warmupTimeout` elapses, set `spec.suspend: false` and remove the LightsOut labels.

### Resources a user suspended

LightsOut leaves a suspended resource alone when it carries no `lightsout.techsupport.mk/managed-by` label. It manages only what it claimed, so a deliberate suspension survives.

## Alert suppression

A suspended resource does not reconcile, so the suspension itself silences most false alerts.

For a Flux `Alert` that fires on label changes, exclude the resources carrying LightsOut labels:

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

## Multi-schedule safety

- LightsOut skips a Flux resource that another schedule already labelled.
- Only the schedule that suspended a resource resumes it.
- The operations are idempotent. Suspending a resource this schedule already suspended does nothing.

## Schedule deletion

The finalizer resumes every Flux resource the schedule suspended before the resource goes away. Nothing stays suspended after its schedule is gone.

## Requirements

The integration targets the stable Flux APIs:

| Resource | API group | Version | Minimum Flux version |
|---|---|---|---|
| Kustomization | `kustomize.toolkit.fluxcd.io` | `v1` | v2.0.0 |
| HelmRelease | `helm.toolkit.fluxcd.io` | `v2` | v2.3.0 |

On Flux older than v2.3.0 a HelmRelease serves `v2beta2` or `v2beta1`, which discovery does not match. Workload scaling continues and the HelmReleases stay unsuspended. Upgrade Flux to v2.3.0 for the full integration.

## Known limitations

`spec.suspend` does not interrupt a reconciliation that is already running. Flux finishes the one in flight and blocks the next. The window is seconds long. A downscale that lands just after a reconcile starts can still see the workloads restored once, before the block takes effect.

## Graceful degradation

- **Flux is not installed.** Discovery returns empty and scaling continues.
- **Flux is older than v2.3.0.** The HelmRelease `v2` CRD is absent, so LightsOut skips HelmReleases. Kustomizations still work on v2.0.0 and later.
- **A Flux operation fails.** LightsOut logs the error and records a `Warning` event on the schedule. Workload scaling never blocks on it.
- **`spec.fluxCD` is absent.** The feature is inactive and costs nothing.
