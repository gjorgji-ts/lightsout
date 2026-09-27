# LightsOut

[![Build](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml/badge.svg)](https://github.com/gjorgji-ts/lightsout/actions/workflows/main-release.yml)
[![Release](https://img.shields.io/github/v/release/gjorgji-ts/lightsout)](https://github.com/gjorgji-ts/lightsout/releases/latest)
[![License](https://img.shields.io/github/license/gjorgji-ts/lightsout)](LICENSE)

**Turn the lights off on your dev clusters at night. Turn them back on before anyone notices.**

LightsOut is a Kubernetes operator that scales workloads down outside business hours and restores them in the morning. You write a cron schedule as a custom resource. LightsOut handles Deployments, StatefulSets, CronJobs, and the databases behind them. Your applications need no changes.

## Why LightsOut

A weekday schedule of 06:00 to 18:00 leaves the cluster idle for about two thirds of the week. Nights and weekends are the largest line on a dev or staging invoice, and nobody is using them.

Cutting that bill takes three steps:

1. **LightsOut scales workloads to zero.** Deployments and StatefulSets go to zero replicas, CronJobs suspend, and operator-managed datastores hibernate.
2. **Your node autoscaler removes the empty nodes.** [Karpenter](https://karpenter.sh/) or Cluster Autoscaler sees nodes that nothing requests, and deprovisions them.
3. **The cloud provider stops charging.** No nodes means no compute bill.

In the morning LightsOut restores the original replica counts, and the autoscaler brings the nodes back.

> [!IMPORTANT]
> The node autoscaler is what saves the money. A workload at zero replicas frees no compute on its own, because the node keeps running and keeps billing. Without an autoscaler that deprovisions, the schedule changes nothing on your invoice.

## Features

- **Cron scheduling** with IANA timezone support
- **Deployments, StatefulSets and CronJobs** scaled to zero or suspended, and restored to the exact values they held
- **Operator-managed datastores** hibernated through their own custom resources, with applications held back until the data layer is ready
- **HPA-aware** disables HPA scale-up during the downscale so the autoscaler cannot undo it
- **Namespace targeting** by label selector, explicit list, or exclusion
- **Namespace-scoped schedules** so a team can set its own hours, which take precedence over a cluster-wide schedule
- **Rate-limited scaling** in batches, to avoid an API spike on large clusters
- **Admission webhooks** that reject invalid schedules and warn about overlaps
- **ArgoCD and FluxCD integration** to stop false alerts and drift correction during the downscale
- **Prometheus metrics** for schedule state, operations, errors and durations

## Quick start

Install with Helm. This command disables the webhooks, which keeps the first install simple:

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
  upscale: "0 6 * * 1-5"        # 06:00 Monday to Friday
  downscale: "0 18 * * 1-5"     # 18:00 Monday to Friday
  timezone: "America/New_York"
  namespaceSelector:
    matchLabels:
      environment: dev
```

Check the state:

```bash
kubectl get lightsoutschedules
```

```text
NAME               STATE   UPSCALE       DOWNSCALE     SUSPENDED   AGE
dev-weekday-hours  Up      0 6 * * 1-5   0 18 * * 1-5  false       7d
```

For an install with webhook validation, see [Setup guide](docs/setup-guide.md).

## How it works

The controller watches two custom resource types:

- **`LightsOutSchedule`** is cluster-scoped, for a platform team that sets cost policy across many namespaces. It targets namespaces by label selector or by name.
- **`LightsOutNamespaceSchedule`** is namespace-scoped, for a team that wants its own hours. A cluster-wide schedule skips any namespace that holds one.

On each reconcile the controller reads the two cron expressions and decides whether the current time falls in the up period or the down period. It then finds the target namespaces and workloads. It records the original replica count in an annotation, so the upscale restores the exact value rather than a guess.

For the internals, see [Architecture](docs/architecture.md).

## Configuration

### `LightsOutSchedule`, cluster-scoped

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `upscale` | string | Yes | Cron expression for the upscale |
| `downscale` | string | Yes | Cron expression for the downscale |
| `timezone` | string | No | IANA timezone (default: `UTC`) |
| `namespaceSelector` | LabelSelector | No | Selects namespaces by label |
| `namespaces` | []string | No | Explicit list of namespace names |
| `excludeNamespaces` | []string | No | Namespaces to leave alone |
| `suspend` | bool | No | Pauses every operation (default: `false`) |
| `workloadTypes` | []string | No | Limits to `Deployment`, `StatefulSet` or `CronJob` |
| `excludeLabels` | LabelSelector | No | Skips workloads carrying these labels |
| `includeOwnedWorkloads` | bool | No | Scales workloads owned by another controller (default: `false`) |
| `upscaleRateLimit` | RateLimitConfig | No | Batches the upscale |
| `downscaleRateLimit` | RateLimitConfig | No | Batches the downscale |
| `argoCD` | ArgoCDConfig | No | Enables [ArgoCD integration](docs/argocd.md) |
| `fluxCD` | FluxCDConfig | No | Enables [FluxCD integration](docs/fluxcd.md) |
| `customResources` | []CustomResourceConfig | No | Turns operator-managed CRs off ([guide](docs/custom-resources.md)) |
| `customResourceWarmupTimeout` | Duration | No | Caps the upscale readiness wait (default: `10m`) |

Set at least one of `namespaceSelector` or `namespaces`.

### `LightsOutNamespaceSchedule`, namespace-scoped

A developer creates this in their own namespace. It carries the same scheduling fields, minus the namespace selection ones, because it always manages the namespace it lives in:

```yaml
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: team-hours
  namespace: team-a
spec:
  upscale: "0 8 * * 1-5"        # 08:00 Monday to Friday
  downscale: "0 20 * * 1-5"     # 20:00 Monday to Friday
  timezone: "Europe/Berlin"
```

A `LightsOutSchedule` that targets this namespace skips it while this resource exists.

### Excluding workloads

`excludeLabels` protects a workload from the schedule:

```yaml
spec:
  excludeLabels:
    matchLabels:
      critical: "true"
```

### Operator-managed workloads

LightsOut skips workloads that carry a controller owner reference, such as a StatefulSet built by a database operator. The owning controller reconciles the replica count straight back, so scaling one to zero starts a fight LightsOut loses. Worse, its own annotation then makes the next reconcile treat the workload as already scaled down.

Turn the operator's custom resource off instead. `spec.customResources` does that, and [Custom resource integration](docs/custom-resources.md) carries a tested recipe for RabbitMQ, ClickHouse, CloudNativePG, ECK, Keycloak, StarRocks, MariaDB, Redis and Strimzi:

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

Some operators have no field that reaches zero. Those must be told to stop reconciling, which leaves LightsOut to scale the workloads itself. They need a `customResources` entry for the pause switch, and:

```yaml
spec:
  includeOwnedWorkloads: true
```

This flag on its own does not hold the workloads down. The pause entry is what stops the owning controller from restoring them.

### Rate limiting

Scale in batches, so a large cluster does not produce an API spike:

```yaml
spec:
  downscaleRateLimit:
    batchSize: 10
    delayBetweenBatches: "5s"
```

### ArgoCD integration

ArgoCD reports a workload at zero replicas as `Degraded` and `OutOfSync`, and `selfHeal` reverts the downscale. The `argoCD` field labels the matching Application so your notification triggers can tell an intentional downscale from a real failure:

```yaml
spec:
  argoCD:
    namespace: argocd    # optional, defaults to "argocd"
```

Labels alone do not stop a sync from undoing the work. ArgoCD also needs `ignoreDifferences` entries and `RespectIgnoreDifferences=true`. For those, see [ArgoCD integration](docs/argocd.md).

### FluxCD integration

FluxCD has no equivalent of `ignoreDifferences`, so the Flux resource itself has to be suspended for the window. The `fluxCD` field sets `spec.suspend: true` on the matching Kustomization and HelmRelease resources, and resumes them on upscale after a warming-up period:

```yaml
spec:
  fluxCD:
    namespace: flux-system    # optional, defaults to "flux-system"
```

For details, see [FluxCD integration](docs/fluxcd.md).

> [!NOTE]
> The FluxCD integration needs RBAC you opt in to. Set `rbac.fluxcd: true` in your Helm values.

## Observability

LightsOut serves these Prometheus metrics on the metrics endpoint:

| Metric | Type | Description |
|--------|------|-------------|
| `lightsout_schedule_state` | Gauge | State per schedule (1=Up, 0=Down) |
| `lightsout_next_transition_seconds` | Gauge | Seconds until the next transition |
| `lightsout_scaling_operations_total` | Counter | Scaling operations by schedule, namespace and type |
| `lightsout_scaling_errors_total` | Counter | Scaling errors |
| `lightsout_managed_workloads` | Gauge | Workloads under management |
| `lightsout_scaling_batches_total` | Counter | Batches processed |
| `lightsout_scaling_workloads_processed_total` | Counter | Workloads processed, by result |
| `lightsout_scaling_duration_seconds` | Histogram | Duration of scaling operations |
| `lightsout_stuck_terminating_pods` | Gauge | Pods past their grace period after a downscale |
| `lightsout_last_reconcile_timestamp_seconds` | Gauge | Unix timestamp of the last reconcile |

LightsOut also records scaling events as Kubernetes Events on the schedule resource.

## Using a node autoscaler

[Karpenter](https://karpenter.sh/) needs no configuration for this. The two tools split the work: LightsOut reads the schedule and empties the workloads, and Karpenter removes the nodes nothing needs.

Karpenter drains and removes the empty nodes within minutes of a downscale, provided its `NodePool` consolidation policy is on. That is the default.

[Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) behaves the same way, as does any autoscaler that deprovisions underused nodes.

## Documentation

- [Architecture](docs/architecture.md) - the reconcile loop and the internals
- [Setup guide](docs/setup-guide.md) - installation with and without webhooks
- [Custom resource integration](docs/custom-resources.md) - hibernate databases and brokers, with a recipe per operator
- [ArgoCD integration](docs/argocd.md) - stop false alerts and prevent selfHeal from reverting a downscale
- [FluxCD integration](docs/fluxcd.md) - suspend Flux resources for the window
- [HPA integration](docs/hpa.md) - how LightsOut stops an HPA from undoing the downscale
- [Security model](docs/security-model.md) - RBAC, risks and mitigations
- [Examples](examples/) - sample schedules

## License

Apache License 2.0. See [LICENSE](LICENSE).
