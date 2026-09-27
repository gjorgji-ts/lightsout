# ArgoCD integration

LightsOut can label ArgoCD Application CRDs while it scales. The ArgoCD UI and your notification rules then tell an intentional downscale from a real failure.

## The problem

ArgoCD compares the live cluster against Git. A Deployment at zero replicas is a mismatch, so ArgoCD reports the Application as `Degraded` and `OutOfSync`. That fills dashboards and alert channels with noise every evening.

Worse, an Application with `selfHeal` enabled reverts the downscale within a reconcile interval, and the schedule still reports success.

## The solution

When a schedule carries `spec.argoCD`, LightsOut labels the matching Applications to mark the downscale as intentional. Your notification triggers and dashboard filters read those labels.

LightsOut does not scale ArgoCD Applications. An Application is a descriptor, not a running workload. The scaling happens on the Deployments, StatefulSets and CronJobs underneath.

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
  argoCD:
    namespace: argocd          # where the Application CRDs live
    warmupTimeout: 10m         # cap on the wait for pod readiness
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `argoCD` | object | `nil` (disabled) | Enables the integration when present |
| `argoCD.namespace` | string | `argocd` | Namespace that holds the Application CRDs |
| `argoCD.warmupTimeout` | duration | `10m` | How long to keep the `warming-up` label after an upscale before removing it, whatever the pod state |

Any value enables the feature, including `{}`. Omitting the field disables it.

## Configuration on the ArgoCD side

The labels handle alerts. They do not stop ArgoCD from detecting drift on the workloads, and they do not stop a sync from undoing the downscale. Both need configuration in ArgoCD itself.

### `ignoreDifferences` for workload fields

Add these to `argocd-cm`, or to your ArgoCD Helm values:

```yaml
resource.customizations.ignoreDifferences.apps_Deployment: |
  jsonPointers:
    - /spec/replicas
    - /metadata/annotations/lightsout.techsupport.mk~1original-replicas
    - /metadata/annotations/lightsout.techsupport.mk~1managed-by
    - /metadata/labels/lightsout.techsupport.mk~1managed-by

resource.customizations.ignoreDifferences.apps_StatefulSet: |
  jsonPointers:
    - /spec/replicas
    - /metadata/annotations/lightsout.techsupport.mk~1original-replicas
    - /metadata/annotations/lightsout.techsupport.mk~1managed-by
    - /metadata/labels/lightsout.techsupport.mk~1managed-by

resource.customizations.ignoreDifferences.batch_CronJob: |
  jsonPointers:
    - /spec/suspend
    - /metadata/annotations/lightsout.techsupport.mk~1original-suspend
    - /metadata/annotations/lightsout.techsupport.mk~1managed-by
    - /metadata/labels/lightsout.techsupport.mk~1managed-by
```

> [!NOTE]
> `~1` is the JSON Pointer escape for `/` inside a key name. See RFC 6901.

`managed-by` appears twice in each list. LightsOut writes that key as a label and as an annotation on every workload it manages. They are two different metadata fields, so each needs its own pointer. Ignore only the label, and the annotation still shows as drift.

Add the HPA kind when a HorizontalPodAutoscaler targets any of your workloads. LightsOut sets `spec.behavior.scaleUp.selectPolicy` to `Disabled` for the window, and records the previous value in an annotation:

```yaml
resource.customizations.ignoreDifferences.autoscaling_HorizontalPodAutoscaler: |
  jsonPointers:
    - /spec/behavior/scaleUp/selectPolicy
    - /metadata/annotations/lightsout.techsupport.mk~1original-hpa-scale-up-policy
    - /metadata/annotations/lightsout.techsupport.mk~1managed-by
```

Without that entry ArgoCD restores `selectPolicy` mid-window, the HPA returns the workload to its original replica count, and the schedule still reports success. For more information, see [HPA integration](hpa.md).

### `ignoreDifferences` for Application CRDs

Applications managed by ArgoCD itself, in the app-of-apps pattern, carry drift of their own, because the labels LightsOut writes are not in Git:

```yaml
resource.customizations.ignoreDifferences.argoproj.io_Application: |
  jsonPointers:
    - /metadata/labels/lightsout.techsupport.mk~1state
    - /metadata/labels/lightsout.techsupport.mk~1managed-by
    - /metadata/annotations/lightsout.techsupport.mk~1warming-up-since
```

LightsOut writes `warming-up-since` on every upscale and removes it when warmup completes. Without that third pointer, the parent Application reports OutOfSync for the length of every warmup window.

Skip this if you create Applications by hand, or if they sit outside an app-of-apps hierarchy.

### Suppressing the Degraded health status

`ignoreDifferences` covers sync status only. ArgoCD still runs its health check against a paused custom resource and reports the parent Application as `Degraded`. The labels stop the notifications, but the UI still shows the Application as unhealthy.

Add the ArgoCD annotation as an extra `setField` on the affected kinds, so LightsOut applies and removes it with the downscale:

```yaml
customResources:
  - group: k8s.mariadb.com
    version: v1alpha1
    kind: MariaDB
    setFields:
      - path: /spec/suspend
        value: true
      - path: /metadata/annotations/argocd.argoproj.io~1ignore-healthcheck
        value: "true"
```

LightsOut treats this annotation as any other captured field. It is absent before the downscale and absent again after the upscale. Add the same pointer to the `ignoreDifferences` list for that kind.

Do not solve this with `resource.customizations.health.<group>_<kind>` and a Lua script that returns `Healthy` for LightsOut-labelled resources. That customization changes the health result for every resource of that kind on the cluster, including the ones that must still report `Degraded`. The annotation above reaches only the resources LightsOut turned off.

> [!WARNING]
> This annotation changes health reporting and nothing else. ArgoCD waits for
> hook completion rather than for health, so `ignore-healthcheck` has no effect
> on a hook. A PreSync or Sync hook that needs a downscaled datastore blocks
> until the upscale. A Keycloak realm-import Job is the common case, because
> `instances: 0` removes the Keycloak the Job connects to. Keep such hooks out
> of downscaled namespaces, or do not sync those Applications during the window.

### The `RespectIgnoreDifferences` sync option

On its own, `ignoreDifferences` only hides the OutOfSync indicator in the UI. A sync still overwrites the downscale. Every Application needs this:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true
```

Without it, a manual sync or an auto-sync restores the Git replica count and undoes the downscale.

## How it works

### Discovery

With the integration enabled, the reconciler:

1. Lists the `argoproj.io/v1alpha1/Application` resources in the configured namespace.
2. Keeps those whose `spec.destination.namespace` is one of the schedule's target namespaces.
3. Returns the matches, for labelling or for cleanup.

### Labels

A managed Application moves through three states:

| State | `lightsout.techsupport.mk/state` | When |
|-------|----------------------------------|------|
| Downscaled | `down` | The workloads sit at zero replicas |
| Warming up | `warming-up` | The workloads are back, but the pods are not ready |
| Up | _(absent)_ | Every workload is healthy, and no labels remain |

The downscale writes two labels:

| Label | Value | Purpose |
|-------|-------|---------|
| `lightsout.techsupport.mk/state` | `down` | Marks the downscale as intentional |
| `lightsout.techsupport.mk/managed-by` | `<schedule-name>` | Names the schedule that owns this Application |

The upscale moves `state` to `warming-up` and adds one annotation:

| Annotation | Value | Purpose |
|------------|-------|---------|
| `lightsout.techsupport.mk/warming-up-since` | RFC3339 timestamp | Records when warmup began, so `warmupTimeout` survives a controller restart |

Once every Deployment and StatefulSet in the namespace has all its replicas ready, or `warmupTimeout` elapses, LightsOut removes both labels and the annotation. The Application returns to its original state.

### Ordering

The order of the label writes against the scaling is what keeps the alert window shut.

On downscale:

1. Label the Applications `down`.
2. Scale the workloads to zero.

On upscale:

1. Scale the workloads back up.
2. Move the Applications to `warming-up`, and stamp `warming-up-since`.
3. Check pod readiness every 30 seconds.
4. Once the pods are ready, or `warmupTimeout` elapses, remove the labels.

ArgoCD therefore learns about the downscale before the pods disappear. On the way back, the pods are running and ready before the suppression signal goes away, which closes the window where startup looks like failure.

### Schedule deletion

The finalizer strips the labels from every Application the schedule managed before the resource goes away. It finds them through the `managed-by` label.

## Multi-schedule safety

Each schedule manages only the Applications it labelled:

- LightsOut skips an Application another schedule already labelled.
- LightsOut skips an Application this schedule already labelled, so repeats are idempotent.
- Only the schedule that labelled an Application removes those labels.

## Using the labels

### Filtering in the UI

Filter the Applications list by label to see what the schedule currently holds down:

```text
lightsout.techsupport.mk/state=down
lightsout.techsupport.mk/state=warming-up
```

### Notification triggers

Add the label check to every trigger you want silenced for the window:

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

An absent label evaluates as an empty string. Both `"" != "down"` and `"" != "warming-up"` are true, so the trigger fires normally outside the window. During the downscale or the warmup one condition is false, and the notification stops.

A success trigger needs the same guard, which is easy to miss. An upscale passes through Healthy and Synced on the way back, and a deployment trigger fires for a release that never happened.

### Grafana and Prometheus

If you export Application labels to Prometheus, through `argocd-metrics` or similar, query on the `state` label to separate an intentional downscale from real degradation.

## RBAC

The integration needs these cluster-wide permissions:

| Resource | API group | Verbs |
|----------|-----------|-------|
| Applications | argoproj.io | get, list, watch, update, patch |

They are not granted by default. Opt in through your Helm values:

```yaml
rbac:
  create: true
  argocd: true
```

Without them the controller cannot list or label Applications. The feature then fails quietly, even with `spec.argoCD` set, and reports warning events on the schedule.

## Graceful degradation

- **ArgoCD is not installed.** The `Application` CRD is absent, discovery returns empty, and scaling continues.
- **A labelling call fails.** LightsOut logs the error and records a warning event on the schedule. Workload scaling never blocks on it.
- **`spec.argoCD` is absent.** The feature is inactive and costs nothing.
