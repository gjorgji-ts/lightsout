# LightsOut

[![Build](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml/badge.svg)](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml)
[![Release](https://img.shields.io/github/v/release/gjorgji-ts/lightsout)](https://github.com/gjorgji-ts/lightsout/releases/latest)
[![License](https://img.shields.io/github/license/gjorgji-ts/lightsout)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/gjorgji-ts/lightsout)](https://goreportcard.com/report/github.com/gjorgji-ts/lightsout)

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
> LightsOut manages the workload layer, while your node autoscaler handles the infrastructure layer.  Together, they eliminate idle compute costs.

## Features

- **Cron-based scheduling** with IANA timezone support
- **Manages Deployments, StatefulSets, and CronJobs** — scales replicas to zero or suspends CronJobs
- **Flexible namespace targeting** — label selectors, explicit lists, and exclusions
- **Rate-limited scaling** — batch workloads to avoid resource spikes
- **Admission webhooks** — validates schedules and detects overlaps before they're applied
- **ArgoCD integration** — optional labeling of ArgoCD Application CRDs to prevent false alerts during downscale
- **Prometheus metrics** — observe schedule state, scaling operations, errors, and durations

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

LightsOut runs as a controller that watches `LightsOutSchedule` custom resources. On each reconciliation, it calculates whether the current time falls in an "up" or "down" period based on your cron expressions, discovers target namespaces, and scales workloads accordingly. Original replica counts are stored in annotations so they can be restored exactly.

For a deeper look at the architecture, see [docs/architecture.md](docs/architecture.md).

## Configuration

### Schedule Spec

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
| `upscaleRateLimit` | RateLimitConfig | No | Rate limit upscale operations |
| `downscaleRateLimit` | RateLimitConfig | No | Rate limit downscale operations |
| `argoCD` | ArgoCDConfig | No | Enable [ArgoCD integration](docs/argocd.md) |

At least one of `namespaceSelector` or `namespaces` must be specified.

### Excluding Workloads

To protect specific workloads from scaling, use `excludeLabels` on the schedule:

```yaml
spec:
  excludeLabels:
    matchLabels:
      critical: "true"
```

Any workloads with matching labels will be skipped during scaling operations.

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
| `lightsout_last_reconcile_timestamp_seconds` | Gauge | Unix timestamp of last reconciliation |

Scaling events are also recorded as Kubernetes Events on the `LightsOutSchedule` resource.

## Using with Karpenter

LightsOut is designed to work seamlessly with [Karpenter](https://karpenter.sh/) to achieve maximum cost savings. There’s no need for any special configuration, the two systems automatically complement each other.

- **LightsOut** monitors cron schedules and scales workloads to zero during off-hours.
- **Karpenter** monitors node utilization and removes nodes that are no longer needed.

As long as Karpenter’s `NodePool` consolidation policy is enabled (the default setting), empty nodes are drained and terminated within minutes of LightsOut scaling workloads down.

This approach also works with [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). Any node autoscaler that deprovisions underutilized nodes will produce the same effect.

## Documentation

- [Architecture](docs/architecture.md) — how LightsOut works internally
- [ArgoCD Integration](docs/argocd.md) — prevent false alerts when scaling down
- [Setup Guide](docs/setup-guide.md) — installation with and without webhooks
- [Security Model](docs/security-model.md) — RBAC, risks, and mitigations
- [Examples](examples/) — sample schedule configurations

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
