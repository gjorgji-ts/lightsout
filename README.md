# LightsOut

[![Build](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml/badge.svg)](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml)
[![Release](https://img.shields.io/github/v/release/gjorgji-ts/lightsout)](https://github.com/gjorgji-ts/lightsout/releases/latest)
[![License](https://img.shields.io/github/license/gjorgji-ts/lightsout)](LICENSE)

LightsOut is a Kubernetes operator that automatically scales down workloads during off-hours and restores them during business hours. This helps platform engineering teams **save over 60% on development and staging cluster costs**.

Define schedules as custom resources, and LightsOut takes care of the rest. You can scale Deployments, StatefulSets, and CronJobs across namespaces according to your configured timetable without any application changes. Original replica counts are automatically preserved and restored.

## Why LightsOut?

Development and staging clusters are typically idle outside business hours, including evenings, nights, and weekends. This equates to approximately 70% of the week, during which you’re paying for compute resources that remain unused.

LightsOut, in conjunction with a node autoscaler like [Karpenter](https://karpenter.sh/), transforms idle time into savings.

1. **LightsOut scales workloads to zero**: Deployments, StatefulSets, and CronJobs are scaled down or suspended according to your schedule.
2. **Karpenter removes empty nodes**: With no workloads requesting resources, Karpenter (or Cluster Autoscaler) deprovisions the underlying nodes.
3. **Cloud provider stops billing**: Since there are no nodes, there are no compute charges.

When business hours resume, LightsOut restores workloads to their original replica counts, and your autoscaler provisions nodes to meet the increased demand.

> [!NOTE]
> LightsOut manages the workload layer, while your node autoscaler handles the infrastructure layer. Together, they eliminate idle compute costs.

## Features

- **Cron-based scheduling** with IANA timezone support
- **Manages Deployments, StatefulSets, and CronJobs** - scales replicas to zero or suspends CronJobs
- **HPA-aware scaling** - automatically patches `spec.minReplicas` on attached HorizontalPodAutoscalers to prevent fight-back, then restores on upscale
- **Flexible namespace targeting** - label selectors, explicit lists, and exclusions
- **Namespace-scoped schedules** - developers can define their own schedules per namespace, independent of global schedules
- **Rate-limited scaling** - batch workloads to avoid resource spikes
- **Admission webhooks** - validates schedules and detects overlaps before they're applied
- **ArgoCD integration** - optional labeling of ArgoCD Application CRDs to prevent false alerts during downscale
- **FluxCD integration** - optional suspension of FluxCD Kustomization and HelmRelease resources to prevent drift detection and alert noise during downscale
- **Operator-managed custom resources** - hibernate databases, brokers and search clusters through their own CRs, restoring the exact values on upscale, with applications held back until the data services are ready
- **Prometheus metrics** - observe schedule state, scaling operations, errors, and durations

## Quick Start

Install with Helm (webhooks disabled for simplicity):

```bash
helm install lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout \
  --set webhook.enabled=false \
  --set certManager.enabled=false
```

Create a schedule:

```yaml
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: dev-weekday-hours
spec:
  upscale: "0 6 * * 1-5"        # 6 AM Monday–Friday
  downscale: "0 18 * * 1-5"     # 6 PM Monday–Friday
  timezone: "America/New_York"
  namespaceSelector:
    matchLabels:
      environment: dev
```

Check status:

```bash
kubectl get lightsoutschedules
```

```
NAME               STATE   UPSCALE       DOWNSCALE     SUSPENDED   AGE
dev-weekday-hours  Up      0 6 * * 1-5   0 18 * * 1-5  false       7d
```

For production setups with webhook validation, see the [Setup Guide](docs/setup-guide.md).

## How It Works

LightsOut runs as a controller that watches two custom resource types:

- **`LightsOutSchedule`** (cluster-scoped) - for platform teams managing cost policies across multiple namespaces. Supports label selectors and explicit namespace lists.
- **`LightsOutNamespaceSchedule`** (namespace-scoped) - for developers who want to define their own schedule for their namespace. When a namespace schedule exists, any global schedule targeting that namespace automatically skips it.

On each reconciliation, the controller calculates whether the current time falls in an "up" or "down" period based on your cron expressions, discovers target namespaces and workloads, and scales accordingly. Original replica counts are stored in annotations so they can be restored exactly.

For a deeper look at the architecture, see [docs/architecture.md](docs/architecture.md).

## Configuration

### `LightsOutSchedule` (cluster-scoped)

Managed by platform teams. Targets workloads across one or more namespaces.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `upscale` | string | Yes | Cron expression for scaling up |
| `downscale` | string | Yes | Cron expression for scaling down |
| `timezone` | string | No | IANA timezone (default: `UTC`) |
| `namespaceSelector` | LabelSelector | No | Select namespaces by label |
| `namespaces` | []string | No | Explicit list of namespace names |
| `excludeNamespaces` | []string | No | Namespaces to exclude |
| `suspend` | bool | No | Pause all operations (default: `false`) |
| `workloadTypes` | []string | No | Filter by type: `Deployment`, `StatefulSet`, `CronJob` |
| `excludeLabels` | LabelSelector | No | Skip workloads matching these labels |
| `includeOwnedWorkloads` | bool | No | Scale workloads owned by another controller (default: `false`) |
| `upscaleRateLimit` | RateLimitConfig | No | Rate limit upscale operations |
| `downscaleRateLimit` | RateLimitConfig | No | Rate limit downscale operations |
| `argoCD` | ArgoCDConfig | No | Enable [ArgoCD integration](docs/argocd.md) |
| `fluxCD` | FluxCDConfig | No | Enable [FluxCD integration](docs/fluxcd.md) |
| `customResources` | []CustomResourceConfig | No | Turn operator-managed CRs off ([guide](docs/custom-resources.md)) |
| `customResourceWarmupTimeout` | Duration | No | Cap on the upscale readiness wait (default: `10m`) |

At least one of `namespaceSelector` or `namespaces` must be specified.

### `LightsOutNamespaceSchedule` (namespace-scoped)

Created by developers in their own namespace. No namespace selection fields - the schedule always manages the namespace it lives in. Supports all the same scheduling fields as `LightsOutSchedule`.

```yaml
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: team-hours
  namespace: team-a
spec:
  upscale: "0 8 * * 1-5"        # 8 AM Monday–Friday
  downscale: "0 20 * * 1-5"     # 8 PM Monday–Friday
  timezone: "Europe/Berlin"
```

When this resource exists in a namespace, any `LightsOutSchedule` targeting that namespace will skip it automatically.

### Excluding Workloads

To protect specific workloads from scaling, use `excludeLabels` on the schedule:

```yaml
spec:
  excludeLabels:
    matchLabels:
      critical: "true"
```

Any workloads with matching labels will be skipped during scaling operations.

### Operator-Managed Workloads

Workloads carrying a controller owner reference are skipped by default. These are created by another operator from its own custom resource, such as a StatefulSet built by a database operator. The owning controller reconciles their replica count, so scaling them to zero only makes it restore them. LightsOut's own annotations then make later reconciles treat them as already scaled down.

Turn the operator's own custom resource off instead, which is what `spec.customResources` does. The [Custom Resource Integration guide](docs/custom-resources.md) carries ready-made stanzas for RabbitMQ, ClickHouse, CloudNativePG, ECK, Keycloak, StarRocks, MariaDB, Redis and Strimzi:

```yaml
spec:
  customResources:
    - group: postgresql.cnpg.io
      version: v1
      kind: Cluster
      setFields:
        - path: /metadata/annotations/cnpg.io~1hibernation
          value: "on"
```

Some operators have no field that reaches zero and must instead be told to stop reconciling, leaving LightsOut to scale the workloads itself. Those need both a `customResources` entry for the pause switch and:

```yaml
spec:
  includeOwnedWorkloads: true
```

Without pausing the owning controller, enabling this on its own will not keep the workloads scaled down.

### Rate Limiting

Gradually scale workloads in batches to avoid resource spikes:

```yaml
spec:
  downscaleRateLimit:
    batchSize: 10
    delayBetweenBatches: "5s"
```

### ArgoCD Integration

If you use ArgoCD, enabling the `argoCD` field prevents ArgoCD from firing false "Degraded" or "OutOfSync" alerts when LightsOut scales workloads down:

```yaml
spec:
  argoCD:
    namespace: argocd    # optional, defaults to "argocd"
```

LightsOut labels ArgoCD Application CRDs with `lightsout.techsupport.mk/state: down` during downscale and removes the labels on upscale. See the [ArgoCD Integration Guide](docs/argocd.md) for details.

### FluxCD Integration

If you use FluxCD, enabling the `fluxCD` field suspends matching Kustomization and HelmRelease resources during downscale, preventing FluxCD from reconciling workloads back to their Git-defined state while they are intentionally scaled to zero:

```yaml
spec:
  fluxCD:
    namespace: flux-system    # optional, defaults to "flux-system"
```

LightsOut sets `spec.suspend: true` on matching Flux resources during downscale and resumes them (with a warming-up grace period) on upscale. See the [FluxCD Integration Guide](docs/fluxcd.md) for details.

> **Note:** You must opt in to the required RBAC by setting `rbac.fluxcd: true` in your Helm values.

## Observability

LightsOut exposes Prometheus metrics on the metrics endpoint:

| Metric | Type | Description |
|--------|------|-------------|
| `lightsout_schedule_state` | Gauge | Current state per schedule (1=Up, 0=Down) |
| `lightsout_next_transition_seconds` | Gauge | Seconds until next state transition |
| `lightsout_scaling_operations_total` | Counter | Total scaling operations by schedule, namespace, type |
| `lightsout_scaling_errors_total` | Counter | Total scaling errors |
| `lightsout_managed_workloads` | Gauge | Number of workloads being managed |
| `lightsout_scaling_batches_total` | Counter | Batches processed during scaling |
| `lightsout_scaling_workloads_processed_total` | Counter | Workloads processed with success/failure result |
| `lightsout_scaling_duration_seconds` | Histogram | Time taken for scaling operations |
| `lightsout_stuck_terminating_pods` | Gauge | Pods still running past their grace period after a downscale, by schedule and namespace |
| `lightsout_last_reconcile_timestamp_seconds` | Gauge | Unix timestamp of last reconciliation |

Scaling events are also recorded as Kubernetes Events on the `LightsOutSchedule` and `LightsOutNamespaceSchedule` resources.

## Using with Karpenter

[Karpenter](https://karpenter.sh/) needs no special configuration here. The two tools divide the work:

- **LightsOut** reads cron schedules and scales workloads to zero during off-hours.
- **Karpenter** watches node utilization and removes nodes nothing needs.

Karpenter drains and terminates the empty nodes within minutes of a downscale, as long as its `NodePool` consolidation policy is on. That is the default.

> [!IMPORTANT]
> The node autoscaler is what saves the money. Scaling a workload to zero frees no compute on its own, because the node keeps running and keeps billing. Pair LightsOut with an autoscaler that deprovisions, or the schedule changes nothing on your invoice.

[Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) works the same way, as does any node autoscaler that deprovisions underused nodes.

## Documentation

- [Architecture](docs/architecture.md) - how LightsOut works internally
- [HPA Integration](docs/hpa.md) - automatic HorizontalPodAutoscaler handling
- [ArgoCD Integration](docs/argocd.md) - prevent false alerts when scaling down
- [FluxCD Integration](docs/fluxcd.md) - prevent drift detection when scaling down
- [Custom Resource Integration](docs/custom-resources.md) - hibernate operator-managed databases and brokers, with per-operator recipes
- [Setup Guide](docs/setup-guide.md) - installation with and without webhooks
- [Security Model](docs/security-model.md) - RBAC, risks, and mitigations
- [Examples](examples/) - sample schedule configurations

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.
