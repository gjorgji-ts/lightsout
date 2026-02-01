# LightsOut

LightsOut is a Kubernetes operator that automatically scales down workloads during off-hours and restores them during business hours. Define schedules as custom resources, and LightsOut handles the rest. Scaling Deployments, StatefulSets, and CronJobs across namespaces on your configured timetable.

No application changes required. Original replica counts are preserved and restored automatically.

## Features

- **Cron-based scheduling** with IANA timezone support
- **Manages Deployments, StatefulSets, and CronJobs** — scales replicas to zero or suspends CronJobs
- **Flexible namespace targeting** — label selectors, explicit lists, and exclusions
- **Rate-limited scaling** — batch workloads to avoid resource spikes
- **Admission webhooks** — validates schedules and detects overlaps before they're applied
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

## Observability

LightsOut exposes Prometheus metrics on the metrics port (default `8080`):

| Metric | Type | Description |
|--------|------|-------------|
| `lightsout_schedule_state` | Gauge | Current state per schedule (1=Up, 0=Down) |
| `lightsout_next_transition_seconds` | Gauge | Seconds until next state transition |
| `lightsout_scaling_operations_total` | Counter | Total scaling operations by schedule, namespace, type |
| `lightsout_scaling_errors_total` | Counter | Total scaling errors |
| `lightsout_managed_workloads` | Gauge | Number of workloads being managed |
| `lightsout_scaling_duration_seconds` | Histogram | Time taken for scaling operations |
| `lightsout_last_reconcile_timestamp_seconds` | Gauge | Unix timestamp of last reconciliation |

Scaling events are also recorded as Kubernetes Events on the `LightsOutSchedule` resource.

## Documentation

- [Architecture](docs/architecture.md) — how LightsOut works internally
- [Setup Guide](docs/setup-guide.md) — installation with and without webhooks
- [Security Model](docs/security-model.md) — RBAC, risks, and mitigations
- [Examples](examples/) — sample schedule configurations

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
