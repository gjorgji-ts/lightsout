# Custom resource integration

LightsOut turns operator-managed custom resources off during the downscale window and restores them on upscale. Databases, message brokers and search clusters then stop costing money overnight, alongside the applications that use them.

## Why workloads alone are not enough

Workloads created by an operator carry a controller owner reference. An ECK `Elasticsearch` owns its StatefulSets, and a CloudNativePG `Cluster` owns its Pods. The scaler skips these by default (see [`includeOwnedWorkloads`](../README.md#operator-managed-workloads)), because the owning operator reconciles their replica count straight back from its custom resource.

Scaling them starts a fight LightsOut loses silently. The workload returns, its `original-replicas` annotation makes the next reconcile treat it as already scaled down, and the status reports success.

Turn the operator's own custom resource off instead. Name a kind and the fields to set in `spec.customResources`. LightsOut captures what those fields held, writes the downscale values, and restores the captured values on upscale.

## How it works

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

On **downscale**, for every matching resource in the schedule's target namespaces:

1. The current value of each `path` is captured into the `lightsout.techsupport.mk/original-fields` annotation on the resource, recording whether the field existed at all.
2. The `value` is written.
3. The resource is labelled `lightsout.techsupport.mk/managed-by` and `lightsout.techsupport.mk/state: down`.

On **upscale**, in this order:

1. Captured values are written back. A field that did not exist before is removed again rather than set to an empty value.
2. The resource moves to `state: warming-up` and LightsOut waits for the workloads in its namespace to report ready.
3. Application workloads scale up only once those are ready, or once `customResourceWarmupTimeout` elapses.

That last step is the point of the ordering. Your applications never start against a database that is still hibernating, or still waiting on a node.

**What counts as ready.** Every Deployment and StatefulSet in the namespace must have all its desired replicas ready. Pods that neither owns must report the `Ready` condition. That is what gates the wait for an operator which builds bare Pods, such as CloudNativePG.

Skipped: workloads already at zero replicas, Job-owned pods (they run to completion and never report Ready), and pods that are finished or terminating.

### Field paths

`path` is an [RFC 6901](https://datatracker.ietf.org/doc/html/rfc6901) JSON Pointer. Two rules matter:

- `~1` escapes a literal `/` inside a segment and `~0` a literal `~`. An annotation key like `cnpg.io/hibernation` is therefore written `/metadata/annotations/cnpg.io~1hibernation`.
- A `*` segment matches every element of an array or every key of an object. `/spec/nodeSets/*/count` therefore covers every node set, whatever their order or number.

LightsOut creates missing objects along the path, which is how a resource with no annotations still gains one. A `*` after a missing segment matches nothing, because there is no shape to expand against.

### Values round-trip exactly

Most of these fields are replica counts whose desired value lives in Git, not in the schedule. A hardcoded upscale value drifts the moment someone changes the cluster size. LightsOut writes back exactly what it found.

## Per-operator recipes

Each stanza below was checked against the operator's current API types, not only its documentation. Add the matching RBAC (see [Permissions](#permissions)).

Every recipe has an end-to-end test that installs the real operator and checks it honours the change. Run one with:

```sh
make test-e2e-operator OPERATOR=cnpg
```

See [test/e2e/README.md](../test/e2e/README.md) for the full list, what each case costs to run, and how to read a failure.

### RabbitMQ

`spec.replicas` is a `*int32` with `Minimum:=0`, so zero is explicit rather than ambiguous with unset. The operator removes the pods and keeps the PVCs.

```yaml
- group: rabbitmq.com
  version: v1beta1
  kind: RabbitmqCluster
  setFields:
    - path: /spec/replicas
      value: 0
```

> [!WARNING]
> A broker with queued messages is not a stateless web tier. Durable queues survive on the PVCs, but anything in flight is lost and producers fail while the cluster is down. Only schedule brokers whose producers can wait until morning.

### ClickHouse (Altinity)

`spec.stop` is a purpose-built hibernate. The operator zeroes every StatefulSet it owns and keeps all PVCs, then rebuilds and reattaches them when the field goes back. The field is a string-bool, so write `"yes"` rather than `true`.

```yaml
- group: clickhouse.altinity.com
  version: v1
  kind: ClickHouseInstallation
  setFields:
    - path: /spec/stop
      value: "yes"
```

This one needs no `includeOwnedWorkloads`, because the operator does the scaling itself.

### CloudNativePG

Declarative hibernation removes the database Pods and keeps the PVCs. The operator does the work, so there is no second step.

```yaml
- group: postgresql.cnpg.io
  version: v1
  kind: Cluster
  setFields:
    - path: /metadata/annotations/cnpg.io~1hibernation
      value: "on"
```

### ECK (Elasticsearch)

ECK cannot be stopped through `spec.nodeSets[].count`. Its validating webhook requires at least one master nodeSet with a non-zero count:

```go
// pkg/controller/elasticsearch/validation/validations.go
seenMaster = seenMaster || (cfg.Node.IsConfiguredWithRole(esv1.MasterRole) &&
    !cfg.Node.IsConfiguredWithRole(esv1.VotingOnlyRole) && ns.Count > 0)
```

Zeroing every nodeSet is rejected with `spec.nodeSets: Required value: Elasticsearch needs to have at least one master node`. Setting `count: 0` works only for a non-master nodeSet in a multi-tier cluster. That scales a data tier. It does not stop the cluster.

The supported route is to pause orchestration and let the scaler zero the StatefulSets ECK owns:

```yaml
- group: elasticsearch.k8s.elastic.co
  version: v1
  kind: Elasticsearch
  setFields:
    - path: /metadata/annotations/eck.k8s.elastic.co~1pause-orchestration
      value: "true"
```

This needs `includeOwnedWorkloads: true` on the schedule, so the scaler zeroes the StatefulSets once ECK stops reconciling them. Pausing keeps certificate rotation, service reconciliation and health monitoring running, so a long pause does not degrade the cluster.

### Keycloak

`spec.instances` is a nullable `Integer`, so zero is explicit. Keycloak keeps its state in an external database, so there is nothing to lose.

```yaml
- group: k8s.keycloak.org
  version: v2beta1
  kind: Keycloak
  setFields:
    - path: /spec/instances
      value: 0
```

> [!NOTE]
> The Keycloak operator's default manifest sets `JOSDK_WATCH_CURRENT`, so it reconciles only its own namespace. If your Keycloak instances live elsewhere, install the `cluster-wide` variant, which sets `JOSDK_ALL_NAMESPACES`.

### StarRocks

Every component's `replicas` is a `*int32` with `Minimum=0`, so zero is valid on all of them. Which components exist depends on the deployment mode, and the entry must match.

**Shared-nothing** (FE plus BE, table data on local disks):

```yaml
- group: starrocks.com
  version: v1
  kind: StarRocksCluster
  setFields:
    - path: /spec/starRocksBeSpec/replicas
      value: 0
    - path: /spec/starRocksCnSpec/replicas
      value: 0
    - path: /spec/starRocksFeSpec/replicas
      value: 0
```

**Shared-data** (FE plus CN, table data in object storage, no BE):

```yaml
- group: starrocks.com
  version: v1
  kind: StarRocksCluster
  setFields:
    - path: /spec/starRocksCnSpec/replicas
      value: 0
    - path: /spec/starRocksFeSpec/replicas
      value: 0
```

A path that matches nothing is skipped with a log line rather than failing. Leaving the `starRocksBeSpec` entry in on a shared-data cluster is therefore harmless. The reverse is not: omit `starRocksCnSpec` on a cluster with CN nodes and the compute tier stays up and billing.

If your object storage runs in-cluster, exclude it with `excludeLabels`. It is an ordinary Deployment with no controller owner reference, so the scaler would otherwise take the store down with the data it holds. It would also return it in no particular order relative to the database that needs it.

> [!NOTE]
> StarRocks documents that FE nodes must not be *resized* below 3, because that breaks quorum. Taking the whole cluster to zero is a full stop, not a partial resize, and it behaves differently. The end-to-end tests check that FE returns cleanly from zero in both modes.

### MariaDB

`spec.replicas` is a non-pointer `int32` with `omitempty` and a default of 1, so setting it to zero serialises to nothing and the default wins. Suspend the operator instead and let the scaler handle the StatefulSet.

```yaml
- group: k8s.mariadb.com
  version: v1alpha1
  kind: MariaDB
  setFields:
    - path: /spec/suspend
      value: true
```

This needs `includeOwnedWorkloads: true` on the schedule, so the scaler zeroes the StatefulSet once the operator stops reconciling it.

### Redis (OpsTree)

The RedisCluster webhook rejects `clusterSize` below 3, so the size field is not a route to zero. Each kind has its own skip-reconcile annotation, gated on the literal string `"true"`.

```yaml
- group: redis.redis.opstreelabs.in
  version: v1beta2
  kind: RedisCluster
  setFields:
    - path: /metadata/annotations/rediscluster.opstreelabs.in~1skip-reconcile
      value: "true"
```

The annotation key differs per kind. The other three are `redis.opstreelabs.in/skip-reconcile`, `redisreplication.opstreelabs.in/skip-reconcile` and `redissentinel.opstreelabs.in/skip-reconcile`. This also needs `includeOwnedWorkloads: true`.

### Strimzi (Kafka)

The only operator here with no off switch. `KafkaNodePool.replicas` does carry `@Minimum(0)`, but the partition-replica check blocks broker scale-down separately. The Strimzi maintainers answer this question by pausing reconciliation and deleting the `StrimziPodSet` resources, then letting the operator rebuild them on resume.

For Strimzi 1.x:

```yaml
- group: kafka.strimzi.io
  version: v1
  kind: Kafka
  setFields:
    - path: /metadata/annotations/strimzi.io~1pause-reconciliation
      value: "true"
- group: core.strimzi.io
  version: v1
  kind: StrimziPodSet
  delete: true
```

Order matters, and it is the declaration order. The pause entry must come before the delete entry, or the operator recreates the pod set immediately. Upscale restores nothing for a `delete` entry. Removing the pause annotation is what makes Strimzi rebuild the pod set from the `Kafka` spec.

> [!CAUTION]
> `delete: true` is the one destructive primitive here. Use it only for resources an operator rebuilds from a durable spec. Deleting a resource that holds the only copy of its own configuration loses it permanently.

## Permissions

The API groups are unknown until you declare them, so RBAC markers cannot generate them. List them in the chart:

```yaml
rbac:
  customResources:
    - apiGroups: ["postgresql.cnpg.io"]
      resources: ["clusters"]
    - apiGroups: ["rabbitmq.com"]
      resources: ["rabbitmqclusters"]
    - apiGroups: ["elasticsearch.k8s.elastic.co"]
      resources: ["elasticsearches"]
    - apiGroups: ["core.strimzi.io"]
      resources: ["strimzipodsets"]
      verbs: ["get", "list", "watch", "delete"]
```

`verbs` defaults to `get, list, watch, update, patch`. Add `delete` for any resource targeted by a `delete: true` entry.

These render into a separate ClusterRole so they stay independent of the generated one.

## Before you schedule this

**Storage keeps billing.** Every mechanism here removes Pods and keeps PersistentVolumeClaims. For Kafka, Elasticsearch and Postgres the storage line is often larger than the compute line. The saving is real, but it is a compute saving, and only if your autoscaler removes the drained nodes.

**Cold start is slower than you expect.** With a node autoscaler, upscale is node provisioning plus a large image pull before the database even starts. Budget minutes, not seconds. The default `customResourceWarmupTimeout` of 10 minutes is usually enough headroom. Raise it if your images are large.

**StatefulSet PVCs and zone topology.** Zone-bound volumes are a known source of pods stuck `Pending` after a down-and-up cycle. Pod zone topology and PVC volume topology can contradict each other. Before you schedule this nightly, check your node pool spans every zone your volumes live in. Then run one manual cycle per data service.

**A missing CRD is not an error.** Discovery returns empty when the operator is absent, and scaling proceeds. The ArgoCD and FluxCD integrations behave the same way.

**Resources are released on deletion.** Deleting the schedule restores every custom resource it turned off, the same way it restores workloads. LightsOut never recreates a resource removed by a `delete` entry. That is the operator's job, once the paused entry alongside it is restored.
