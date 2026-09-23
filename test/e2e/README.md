# End-to-end tests

Two suites live here, split by build tag.

| Suite | Tag | Command |
|---|---|---|
| Core behaviour | `e2e` | `make test-e2e` |
| Operator integration | `e2e operators` | `make test-e2e-operator OPERATOR=<key>` |

Both share `e2e_suite_test.go`. It builds the manager image, loads it into Kind, installs cert-manager and the CRDs, and deploys the controller.

## Core behaviour tests

Scaling, namespace targeting, HPA, safety protections, webhooks, schedule lifecycle and namespace schedules. Run them with `make test-e2e`, which takes roughly 26 minutes.

One limit is worth knowing. `Stuck Termination` covers only the negative case: a clean downscale must report no stuck pod and emit no warning.

The positive case needs a pod that is terminating, past its grace period, and still `Running`. Every route to that state ends in `Failed`, because the kubelet always wins:

- A finalizer holds the API object, but not the containers.
- A zero grace period means an immediate SIGKILL.
- An unschedulable pod moves to `Failed` on delete.
- No process can ignore SIGKILL.

It needs a broken kubelet, which no test can arrange. `TestReconcile_FindsPodStuckAfterTheDownscale` covers that path against the real reconcile loop instead.

## Operator integration tests

Each case installs a **real operator**, lets it provision a real data plane, then drives a `LightsOutSchedule` through a downscale and an upscale. This is the only way to know a recipe in [`docs/custom-resources.md`](../../docs/custom-resources.md) works rather than merely parses.

Each case asserts, in order:

1. The operator provisions the expected pods.
2. The target field starts at its expected value.
3. After downscale, lightsout writes the off value and claims the resource with a `managed-by` label.
4. **The operator removes its pods.** A failure here is a wrong recipe, not a lightsout bug.
5. After upscale, lightsout restores the captured original value.
6. **The operator rebuilds its pods.**
7. Lightsout releases the resource once warmup completes.

Each case also creates the ClusterRole for that operator's API group. It is the same grant the `rbac.customResources` Helm value renders in a real install.

### Pod existence versus readiness

Steps 1 and 6 count pods. Cases marked **ready-gated** count only pods reporting the `Ready` condition. The rest count any pod object.

The distinction matters. A pod stuck `Pending` satisfies a bare existence check, so the test would prove only that the operator reacted to the restored spec. Step 4 always counts existence, because a pod going away is a pod going away.

Ready-gating is off in two situations:

- The pod never reaches Ready in this environment (Keycloak on `dev-file`).
- No run confirms yet that it does (both StarRocks cases). FE readiness depends on the compute tier registering, and these are the slowest cases to re-run.

Turning it on for those is a one-line change.

### Running them

`make test-e2e` excludes them. Installing nine operators and their data planes at once exhausts most machines. Run one at a time:

```sh
make test-e2e-operator OPERATOR=cnpg
```

`OPERATOR` accepts one key, a comma-separated list, or `all`:

```sh
make test-e2e-operator OPERATOR=rabbitmq,clickhouse,keycloak
make test-e2e-operator OPERATOR=all      # needs a large machine
```

With `OPERATOR=all` the cases continue after one fails, so a single broken recipe does not hide the rest. A full run takes about 34 minutes on a laptop, plus roughly a minute of suite setup.

Case times vary with machine load and with what the image cache holds. `redis` finishes in about a minute. `strimzi` and the two StarRocks cases take several. A first pull of a multi-GB image takes longer still.

> [!NOTE]
> The Kind cluster is reused between runs and is never torn down automatically. Remove it with `make cleanup-test-e2e`.

### Keeping runtime down

**Batch the cases into one invocation.** Setup and teardown cost 70-110s *per invocation*. That covers building and loading the image, installing cert-manager, deploying the controller, then removing all of it. Ten invocations pay it ten times. A list pays it once:

```sh
make test-e2e-operator OPERATOR=redis,rabbitmq,mariadb
```

**Reuse the deployment while iterating.** `E2E_REUSE=true` leaves cert-manager, the CRDs and the controller in place, and restarts the controller next time instead of reinstalling. A rebuilt image still takes effect, because the rollout restarts after the image is loaded:

```sh
make test-e2e-operator OPERATOR=cnpg E2E_REUSE=true
```

Namespace teardown no longer blocks. Waiting for finalizers and PVCs cost roughly 450s across the suite, 197s for `eck` alone, and verified nothing. Each case deletes its custom resource first, so the operator can run its finalizers while still installed. The namespace then goes with `--wait=false`.

### Namespace layout

Each case spans two namespaces, the way these are deployed for real:

- **Operator namespace** holds the operator, a platform component.
- **Workload namespace** (`lightsout-op-<key>`) holds the data plane. The schedule targets only this one.

Keeping them apart is not cosmetic. An operator Deployment carries no controller owner reference. In a shared namespace the scaler would take the operator down with the data plane, and the test would pass for the wrong reason.

| Key | Operator | Operator namespace | Pods | Rough memory | Notes |
|---|---|---|---|---|---|
| `rabbitmq` | RabbitMQ | `rabbitmq-system` | 1 | ~250Mi | Lightest. Start here. Ready-gated |
| `clickhouse` | ClickHouse (Altinity) | `kube-system` | 1 | ~500Mi | Bundle hardcodes `kube-system`. Ready-gated |
| `cnpg` | CloudNativePG | `cnpg-system` | 1 | ~300Mi | Ready-gated |
| `redis` | Redis (OpsTree) | `redis-operator` | 1 | ~150Mi | Needs Helm. Pause-plus-scale. Ready-gated |
| `mariadb` | MariaDB | `mariadb-operator` | 1 | ~500Mi | Needs Helm. Pause-plus-scale. Ready-gated |
| `keycloak` | Keycloak | `keycloak-operator` | 1 | ~700Mi | Uses `dev-file`. The pod may never report Ready |
| `strimzi` | Strimzi (Kafka) | `strimzi-system` | 1 | ~1.5Gi | The only case exercising `delete: true`. Ready-gated |
| `eck` | ECK (Elasticsearch) | `elastic-system` | 1 | ~2Gi | mmap disabled for Kind. Pause-plus-scale. Ready-gated |
| `starrocks` | StarRocks (shared-nothing) | `starrocks` | 2 | ~6Gi | Two multi-GB pulls, 25m window. Verifies FE behaviour at zero |
| `starrocks-shared` | StarRocks (shared-data) | `starrocks` | 2 + SeaweedFS | ~6Gi | Storage-compute separation. Three pulls, 25m window |

Namespace choices are fixed by upstream, not by preference:

- `cnpg-system`, `elastic-system`, `rabbitmq-system` and `starrocks` come from the operators' own manifests.
- `kube-system` is where the ClickHouse install bundle hardcodes itself. Relocating it needs the template variant plus envsubst.
- `keycloak` is **mandatory**. `kubernetes.yml` hardcodes `namespace: keycloak` in its ClusterRoleBinding subject, so any other namespace leaves that binding pointing at a missing ServiceAccount.
- `mariadb-operator` and `redis-operator` follow each project's documented convention.
- Strimzi enforces nothing. Its quickstart co-locates operator and cluster in `kafka`. This harness splits them.

Two operators need more than a plain apply:

- **Keycloak** ships a namespaced manifest that sets `JOSDK_WATCH_CURRENT`. The case installs the `cluster-wide` kustomization with `kubectl apply -k`, because its ClusterRoleBindings carry no subject namespace and only the kustomization's NamespaceTransformer fills them in.
- **StarRocks** ships its CRDs as separate release assets, not inside `operator.yaml`.

Most operators watch cluster-wide. **Strimzi is the exception.** It defaults `STRIMZI_NAMESPACE` to its own `metadata.namespace`, so it watches only where it runs. The case installs it into `strimzi-system`, repoints `STRIMZI_NAMESPACE` at the workload namespace, and creates the three RoleBindings it needs there: `strimzi-cluster-operator`, `strimzi-cluster-operator-watched`, and `strimzi-cluster-operator-entity-operator-delegation`. That is the documented way to run one cluster operator across tenant namespaces.

### StarRocks deployment modes

StarRocks runs two ways, and they hibernate differently. Both are covered:

- **`starrocks`** is shared-nothing. FE plus BE, table data on local disks. Downscale zeroes `starRocksFeSpec`, `starRocksBeSpec` and `starRocksCnSpec`.
- **`starrocks-shared`** is shared-data. FE plus CN, table data in object storage, no BE. Downscale zeroes `starRocksFeSpec` and `starRocksCnSpec`.

The shared-data case needs an S3 endpoint, so it first creates SeaweedFS in the workload namespace. That is an all-in-one deployment, a Service, and a Job that creates the bucket.

`run_mode = shared_data` is an `fe.conf` setting rather than a CRD field, so the FE config arrives through a ConfigMap named by `configMapInfo`.

`excludeLabels` matching `lightsout-e2e/role: storage` keeps SeaweedFS out of the schedule. It is a plain Deployment with no controller owner reference, so the scaler would otherwise take the object store down with the data it backs. Copy the exclusion if you run in-cluster object storage. A real deployment points at an external bucket instead.

### Status

All ten cases pass against their real operators, with ready-gating on where the table says so.

- **StarRocks FE returns from zero in both modes.** The upstream warning about resizing FE below 3 covers a shrinking cluster, not a stopped one.
- **Strimzi pause-then-delete works.** Pods go in ~50s and the operator rebuilds the StrimziPodSet on resume. Strimzi 1.x serves only `kafka.strimzi.io/v1`. On the 0.4x line set `STRIMZI_API_VERSION=v1beta2`.
- **Ready-gating changes what the assertions mean.** On `cnpg` provisioning went from ~10s to ~50s and restore from under a second to ~20s. The earlier numbers timed the pod object appearing, not Postgres accepting connections.

### Version overrides

Defaults are pinned so a broken upstream release cannot silently change what the tests mean.

| Variable | Default |
|---|---|
| `CNPG_VERSION` | `1.30.0` |
| `ECK_VERSION` | `3.5.0` |
| `ELASTIC_STACK_VERSION` | `9.5.4` |
| `RABBITMQ_OPERATOR_VERSION` | `v2.23.0` |
| `CLICKHOUSE_OPERATOR_VERSION` | `release-0.27.3` |
| `KEYCLOAK_VERSION` | `26.7.4` |
| `STARROCKS_OPERATOR_VERSION` | `v1.11.7` |
| `MARIADB_OPERATOR_VERSION` | `26.10.1` |
| `REDIS_OPERATOR_VERSION` | `0.26.1` |
| `KAFKA_VERSION` | `4.3.1` |
| `STARROCKS_IMAGE_TAG` | `4.1-latest` |
| `SEAWEEDFS_IMAGE_TAG` | `4.47` |
| `REDIS_IMAGE_TAG` | `v8.10.1` |
| `STRIMZI_API_VERSION` | `v1` (use `v1beta2` for Strimzi 0.4x) |

```sh
make test-e2e-operator OPERATOR=eck ELASTIC_STACK_VERSION=8.18.2
```

### Prerequisites

- `kind`, `kubectl`, `docker`
- `helm`, for the `mariadb` and `redis` cases only
- Outbound network access. Every case pulls its operator manifest from upstream.

### When a case fails

The assertion messages name which half failed.

| Symptom | Cause |
|---|---|
| No workloads, and the CR `status` names a reason | Read the reason. StarRocks reports `spec.template.spec.containers[0].image: Required value` when the CR omits an image, because only its Helm chart supplies defaults |
| Lightsout did not patch the custom resource | Check `kubectl logs -n lightsout-system deploy/lightsout-controller-manager`. Usually a ClusterRole missing a resource name, which appears as `forbidden` on list or update |
| No workloads, and the workload namespace has no events | The operator is not watching that namespace. For Strimzi, check `STRIMZI_NAMESPACE` and the RoleBindings |
| The operator ignored the downscale | The recipe is wrong for this operator version. Check whether the field moved, and whether the operator logged a rejection |
| The operator did not rebuild after restore | The restore value is right, but the operator will not come back from it. This is the interesting failure, and the one that should change the recipe |
| Lightsout kept the resource in warming-up | The data pods never reported Ready. Expected for `keycloak`, which is why `customResourceWarmupTimeout` is 5m here. The release assertion allows for it |

Nothing is torn down until you run `make cleanup-test-e2e`, so leave the cluster up and inspect it.
