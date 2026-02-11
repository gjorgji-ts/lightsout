# Architecture

LightsOut is a Kubernetes operator built with [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime). It watches custom `LightsOutSchedule` resources and automatically scales workloads up or down based on cron schedules.

This document explains how the system works internally.

## Overview

A Kubernetes operator is a controller that extends the Kubernetes API with custom resources (CRDs) and reconciliation logic. Instead of running imperative scripts on a timer, you declare your desired scaling schedule as a `LightsOutSchedule` resource, and the operator continuously ensures the cluster matches that intent.

The core loop is:

1. User creates a `LightsOutSchedule` CR
2. The controller detects the change and runs its reconciliation logic
3. It calculates whether the current time falls in an "up" or "down" period
4. It discovers which namespaces and workloads are in scope
5. It scales workloads accordingly, storing original state in annotations
6. It updates the schedule's status with current state and next transition times
7. It re-queues itself to reconcile again at the next transition time (or sooner if rate-limited batch processing is in progress)

## Component Map

```mermaid
flowchart TD
    CR["LightsOutSchedule CR"] --> R["Reconciler"]
    R --> PC["Period Calculator"]
    R --> ND["Namespace Discovery"]
    PC -->|"current state + next transition"| R
    ND -->|"target namespaces"| R
    R --> AL["ArgoCD Labeler<br/>(optional)"]
    R --> WS["Workload Scaler<br/>(budget-based rate limiting)"]
    AL -->|"label/unlabel apps"| ArgoCD["ArgoCD Application CRDs"]
    WS -->|"scale operations"| K8s["Kubernetes API<br/>(Deployments, StatefulSets, CronJobs)"]
    WS -->|"store/restore state"| Ann["Annotations<br/>original-replicas<br/>managed-by"]
    R -->|"emit"| Ev["Kubernetes Events"]
    R -->|"expose"| Met["Prometheus Metrics"]
    R -->|"update"| Status["Schedule Status"]
```

## Components

### Reconciler

The central controller loop (`internal/controller/lightsoutschedule_controller.go`). On each reconciliation cycle it:

- Reads the `LightsOutSchedule` spec
- Delegates to the Period Calculator to determine current state
- Delegates to Namespace Discovery to find target namespaces
- Collects workloads (Deployments, StatefulSets, CronJobs) across those namespaces
- Filters out excluded workloads via `excludeLabels`
- If ArgoCD integration is enabled, labels/unlabels ArgoCD Application CRDs (ordered relative to scaling)
- Delegates to the Workload Scaler for actual scaling (with budget-based rate limiting when configured)
- Updates the schedule's status and conditions
- Re-queues for the next transition time, or sooner if a batch limit was reached

A finalizer (`lightsout.techsupport.mk/cleanup`) ensures that when a schedule is deleted, all managed workloads are restored to their original state before the resource is removed.

### Period Calculator

Determines the current scaling state (`internal/controller/period.go`). Given the `upscale` and `downscale` cron expressions plus a timezone, it calculates:

- Whether the current moment is in an "up" or "down" period
- When the next upscale and downscale transitions will occur

It uses adaptive search windows based on cron frequency to efficiently find the next matching times, and caches results to avoid redundant computation.

### Namespace Discovery

Resolves which namespaces are in scope (`internal/controller/namespace.go`). It supports three targeting mechanisms that can be combined:

- **Label selectors** (`namespaceSelector`) — select namespaces by labels
- **Explicit lists** (`namespaces`) — name specific namespaces
- **Exclusions** (`excludeNamespaces`) — remove namespaces from the result

System namespaces (`kube-system`, `kube-public`, `kube-node-lease`) are always excluded automatically.

### Workload Scaler

Handles the actual scaling of Kubernetes workloads (`internal/controller/scaler.go`):

- **Deployments and StatefulSets** — scales replicas to 0 on downscale; restores from the `original-replicas` annotation on upscale
- **CronJobs** — suspends on downscale; unsuspends on upscale (only if LightsOut was the one that suspended it)

Key design properties:

- **Idempotent** — safe to retry. If a workload is already scaled down, it won't be touched again.
- **Respects user intent** — if a user manually scales a workload while it's managed, LightsOut tracks this and won't overwrite user changes.
- **Managed-by tracking** — each workload is annotated with the schedule name that manages it, preventing conflicts between schedules.

### Rate Limiting

Prevents resource spikes during bulk scaling. When rate limiting is configured (`batchSize` and optional `delayBetweenBatches`), the reconciler uses a **non-blocking budget-based approach**: it processes up to `batchSize` actual scale operations per reconciliation cycle, then returns early and requeues itself after the configured delay. On the next reconcile, it re-lists workloads and continues — already-processed workloads are skipped cheaply via their annotations without consuming budget.

This design keeps the controller responsive during large-scale operations. Spec changes, suspension, deletion, and period transitions (e.g., an upscale time arriving mid-downscale) are all picked up on the next requeue rather than being blocked until all batches finish. The requeue delay is `min(delayBetweenBatches, timeUntilNextTransition)` to ensure period transitions are never missed.

### ArgoCD Labeler

Optional component that labels ArgoCD Application CRDs during scaling operations (`internal/controller/argocd.go`). When `spec.argoCD` is set on a schedule, the labeler:

- **Discovers** ArgoCD `Application` CRDs in the configured namespace (default: `argocd`)
- **Filters** applications whose `spec.destination.namespace` matches the schedule's target namespaces
- **Labels** matching applications with `lightsout.techsupport.mk/state: down` and `lightsout.techsupport.mk/managed-by: <schedule>` during downscale
- **Removes** those labels during upscale

This uses an unstructured client to avoid any compile-time dependency on ArgoCD. If the ArgoCD CRD is not installed on the cluster, the labeler gracefully skips with a log message.

Execution is ordered to prevent false alerts:
- **Downscale**: label ArgoCD apps first, then scale workloads
- **Upscale**: scale workloads first, then remove ArgoCD labels

ArgoCD errors are best-effort — they are logged and emitted as events but never block workload scaling.

See the [ArgoCD Integration Guide](argocd.md) for usage details.

## Key Design Decisions

### Cluster-Scoped Resource

`LightsOutSchedule` is currently a cluster-scoped resource. This is intentional for organizations that want centralized cost policies — a platform team defines schedules that span multiple namespaces. Namespace-scoped scheduling is planned for future releases to support team-level self-service.

### Annotation-Based State

Original replica counts and management metadata are stored directly on the workloads as annotations. This avoids the need for an external database and ensures state stays co-located with the resources it describes. If the operator is uninstalled, the annotations remain harmless.

### Finalizer for Cleanup

A finalizer on each `LightsOutSchedule` ensures that deleting a schedule restores all managed workloads first. Without this, deleting a schedule while workloads are scaled down would leave them at zero replicas permanently.

### Idempotent Scaling

Every scaling operation checks current state before acting. This means:

- Partial failures during a reconciliation are safe — the next cycle picks up where it left off
- Multiple reconciliations in quick succession don't cause issues
- The controller can be restarted at any time without data loss

### Soft ArgoCD Dependency

ArgoCD integration uses Kubernetes unstructured objects instead of importing ArgoCD Go types. This means:

- The operator compiles without any `argoproj.io` dependency
- It runs normally on clusters without ArgoCD installed
- RBAC permissions for `argoproj.io/applications` are requested but unused if ArgoCD is absent
- The feature is entirely opt-in via the `spec.argoCD` field

## Webhooks

LightsOut includes optional admission webhooks for validation and defaulting:

**Mutating webhook** — sets `timezone` to `UTC` if not specified.

**Validating webhook** — rejects invalid schedules before they're persisted:
- Validates cron expressions for both `upscale` and `downscale`
- Validates the timezone is a recognized IANA timezone
- Ensures at least one namespace selection method is configured
- Validates rate limit configurations (batch size > 0, non-negative delay)
- Validates ArgoCD namespace is a valid DNS label when provided
- Warns (but does not reject) when schedules overlap with existing ones

## Metrics

LightsOut exposes Prometheus metrics via the controller-runtime metrics server:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `lightsout_schedule_state` | Gauge | `schedule` | Current state (1=Up, 0=Down) |
| `lightsout_next_transition_seconds` | Gauge | `schedule`, `transition_type` | Seconds until next transition |
| `lightsout_scaling_operations_total` | Counter | `schedule`, `namespace`, `workload_type`, `operation` | Total scaling operations |
| `lightsout_scaling_errors_total` | Counter | `schedule`, `namespace`, `workload_type` | Total scaling errors |
| `lightsout_managed_workloads` | Gauge | `schedule`, `workload_type` | Managed workload count |
| `lightsout_scaling_batches_total` | Counter | `schedule`, `direction` | Batches processed |
| `lightsout_scaling_workloads_processed_total` | Counter | `schedule`, `direction`, `result` | Workloads processed |
| `lightsout_scaling_duration_seconds` | Histogram | `schedule`, `direction` | Scaling operation duration |
| `lightsout_last_reconcile_timestamp_seconds` | Gauge | `schedule` | Last reconcile timestamp |
