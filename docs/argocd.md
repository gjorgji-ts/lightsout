# ArgoCD Integration

LightsOut can optionally label ArgoCD Application CRDs during scaling operations so that ArgoCD UIs and notifications reflect intentional downscale states. This prevents false "Degraded" or "OutOfSync" alerts when workloads are scaled to zero during off-hours.

## The Problem

When LightsOut scales Deployments to zero replicas, ArgoCD sees a mismatch between the desired state (defined in Git) and the live state (0 replicas). This causes ArgoCD to report the application as degraded or out of sync, which generates noise in dashboards and alert channels.

## The Solution

When `spec.argoCD` is set on a schedule, LightsOut labels matching ArgoCD Application CRDs with metadata that signals the downscale is intentional. ArgoCD notification templates and dashboard filters can then use these labels to suppress or annotate alerts for applications that are in a known downscaled state.

LightsOut does **not** scale ArgoCD Applications themselves. ArgoCD Applications are declarative descriptors, not running workloads. The actual scaling continues to happen at the Deployment/StatefulSet/CronJob level.

## Configuration

Add the `argoCD` field to your schedule:

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
  argoCD:                      # presence of this block enables the feature
    namespace: argocd          # defaults to "argocd" if omitted
    warmupTimeout: 10m         # defaults to 10m if omitted
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `argoCD` | object | `nil` (disabled) | When present, enables ArgoCD integration |
| `argoCD.namespace` | string | `argocd` | Namespace where ArgoCD Application CRDs live |
| `argoCD.warmupTimeout` | duration | `10m` | How long to keep the `warming-up` label after upscale before removing it regardless of pod readiness |

Setting `argoCD` to any value (even `{}`) enables the feature. Omitting it entirely disables it.

## ArgoCD-Side Configuration

LightsOut labels ArgoCD Application CRDs to signal downscale state, but ArgoCD also needs configuration changes to prevent drift detection on the fields LightsOut modifies on workloads.

### `ignoreDifferences` for Workload Fields

When LightsOut scales a Deployment to 0 replicas, ArgoCD compares the live state against Git and sees a mismatch. The labels on Application CRDs help with notification suppression and UI filtering, but they do not prevent ArgoCD from detecting drift on the workloads themselves. You need `ignoreDifferences` for that.

Add these to your ArgoCD ConfigMap (`argocd-cm`) or Helm values:

```yaml
resource.customizations.ignoreDifferences.apps_Deployment: |
  jsonPointers:
    - /spec/replicas
    - /metadata/annotations/lightsout.techsupport.mk~1original-replicas
    - /metadata/labels/lightsout.techsupport.mk~1managed-by

resource.customizations.ignoreDifferences.apps_StatefulSet: |
  jsonPointers:
    - /spec/replicas
    - /metadata/annotations/lightsout.techsupport.mk~1original-replicas
    - /metadata/labels/lightsout.techsupport.mk~1managed-by

resource.customizations.ignoreDifferences.batch_CronJob: |
  jsonPointers:
    - /spec/suspend
    - /metadata/annotations/lightsout.techsupport.mk~1original-suspend
    - /metadata/labels/lightsout.techsupport.mk~1managed-by
```

> **Note:** `~1` is the JSON Pointer (RFC 6901) escape for `/` in key names.

### `ignoreDifferences` for Application CRDs (App-of-Apps)

If your ArgoCD Applications are themselves managed by ArgoCD (app-of-apps pattern), the labels LightsOut adds to Application CRDs are also drift from Git's perspective. Add:

```yaml
resource.customizations.ignoreDifferences.argoproj.io_Application: |
  jsonPointers:
    - /metadata/labels/lightsout.techsupport.mk~1state
    - /metadata/labels/lightsout.techsupport.mk~1managed-by
```

This is only needed if Applications are managed declaratively through Git. If you create Applications manually or they are not part of an app-of-apps hierarchy, you can skip this.

### `RespectIgnoreDifferences` Sync Option

By default, `ignoreDifferences` only suppresses the OutOfSync indicator in the UI — a sync operation will still overwrite LightsOut's changes. To prevent this, each Application must include:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true
```

Without this, a manual sync or auto-sync will restore replicas back to the Git-defined value, undoing LightsOut's downscale.

## How It Works

### Discovery

When ArgoCD integration is enabled, the reconciler:

1. Lists all `argoproj.io/v1alpha1/Application` CRDs from the configured ArgoCD namespace
2. Filters to applications whose `spec.destination.namespace` is in the schedule's resolved target namespaces
3. Returns matched applications for labeling or unlabeling

### Labels Applied

LightsOut uses a three-state lifecycle on matching ArgoCD Application CRDs:

| State | `lightsout.techsupport.mk/state` | When |
|-------|----------------------------------|------|
| Downscaled | `down` | Workloads are at 0 replicas |
| Warming up | `warming-up` | Workloads have been scaled back up but pods are not yet all Ready |
| Up (normal) | _(absent)_ | All workloads are healthy; no labels present |

During downscale, LightsOut adds these labels to matching ArgoCD Application CRDs:

| Label | Value | Purpose |
|-------|-------|---------|
| `lightsout.techsupport.mk/state` | `down` | Signals the application is intentionally downscaled |
| `lightsout.techsupport.mk/managed-by` | `<schedule-name>` | Identifies which schedule manages this application |

During upscale, the `state` label transitions to `warming-up` and the following annotation is added:

| Annotation | Value | Purpose |
|------------|-------|---------|
| `lightsout.techsupport.mk/warming-up-since` | RFC3339 timestamp | Records when warming-up began; used to enforce `warmupTimeout` across controller restarts |

Once all Deployments and StatefulSets in the target namespace have all their desired replicas ready — or the `warmupTimeout` elapses — both labels and the annotation are removed, leaving the Application CRD pristine.

### Execution Ordering

Labels are applied and removed in a specific order relative to workload scaling to minimize the window where ArgoCD could fire false alerts:

**Downscale:**
1. Label ArgoCD apps as `down`
2. Scale workloads to zero

**Upscale:**
1. Scale workloads back up
2. Transition ArgoCD apps from `down` → `warming-up` (adds `warming-up-since` timestamp)
3. Requeue every 30 seconds to check pod readiness
4. Once all pods are ready (or `warmupTimeout` elapses), remove all labels

This ordering ensures that:
- On downscale, ArgoCD knows the app is being intentionally scaled down *before* pods disappear
- On upscale, pods are fully running and ready *before* the suppression signal is removed, eliminating the false-alert window during pod startup

### Schedule Deletion

When a `LightsOutSchedule` or `LightsOutNamespaceSchedule` is deleted, the finalizer cleanup also removes any labels from ArgoCD Applications that were managed by that schedule. This uses the `managed-by` label for efficient lookup.

## Graceful Degradation

ArgoCD integration is designed to never block core functionality:

- **ArgoCD not installed**: If the `argoproj.io/v1alpha1/Application` CRD does not exist on the cluster, discovery returns empty and scaling proceeds normally.
- **ArgoCD labeling fails**: Errors are logged and emitted as Kubernetes warning events on the schedule, but workload scaling continues unblocked.
- **ArgoCD integration disabled**: When `spec.argoCD` is `nil` (omitted), the feature is completely inactive with zero overhead.

## Multi-Schedule Safety

If multiple schedules target overlapping namespaces, each schedule only manages ArgoCD Applications it has labeled:

- An application already labeled by a different schedule is skipped
- An application already labeled by the same schedule is skipped (idempotent)
- Only the schedule that labeled an application can remove its labels

## Using Labels in ArgoCD

### Filtering in ArgoCD UI

You can filter applications in the ArgoCD UI by label to see which apps are in a managed state:

```
lightsout.techsupport.mk/state=down
lightsout.techsupport.mk/state=warming-up
```

### Notification Triggers

ArgoCD notification triggers can check for the LightsOut label to suppress alerts during downscale and warmup. Add the label check to each trigger you want to suppress:

```yaml
trigger.on-health-degraded: |
  - when: app.status.health.status == 'Degraded'
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'down'
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'warming-up'
    send: [app-health-degraded]

trigger.on-sync-failed: |
  - when: app.status.operationState.phase == 'Failed'
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'down'
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'warming-up'
    send: [app-sync-failed]

trigger.on-progress-stuck: |
  - when: app.status.health.status == 'Progressing'
      and time.Now().Sub(time.Parse(app.status.operationState.startedAt)).Minutes() >= 10
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'down'
      and app.metadata.labels['lightsout.techsupport.mk/state'] != 'warming-up'
    send: [app-progress-stuck]
```

When the label is absent (normal upscaled state), the expression evaluates the label as an empty string. `"" != "down"` and `"" != "warming-up"` are both `true`, so the trigger fires normally. During downscale or warmup the respective condition is `false` and the notification is suppressed.

### Grafana / Prometheus

If you export ArgoCD Application labels to Prometheus (e.g., via `argocd-metrics`), you can use the `state` label in Grafana queries to distinguish intentional downscale from real degradation.

## RBAC

The ArgoCD integration requires these additional cluster-wide permissions:

| Resource | API Group | Verbs |
|----------|-----------|-------|
| Applications | argoproj.io | get, list, watch, update, patch |

These permissions are **not** included by default. You must opt in by setting `rbac.argocd: true` in your Helm values:

```yaml
rbac:
  create: true
  argocd: true
```

Without this, the controller will not have permission to list or label ArgoCD Application CRDs, and ArgoCD integration will silently fail even if `spec.argoCD` is set on a schedule. Errors are emitted as Kubernetes warning events on the schedule resource.
