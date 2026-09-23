//go:build e2e && operators
// +build e2e,operators

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Operator integration e2e tests.
//
// Each case installs a real operator, lets it provision real workloads, then
// drives a LightsOutSchedule through a downscale and an upscale. This is the only
// way to know a recipe in docs/custom-resources.md works rather than parses.
//
// `make test-e2e` excludes them: a full operator and its data plane per case is
// too heavy to run at once. Run them one at a time:
//
//	make test-e2e-operator OPERATOR=cnpg
//
// See docs/custom-resources.md for what each recipe does, and test/e2e/README.md
// for the footprint of each case.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gjorgji-ts/lightsout/test/utils"
)

// Operator versions. Overridable so a stale default can be worked around without
// editing the test.
var (
	cnpgVersion       = envOr("CNPG_VERSION", "1.30.0")
	eckVersion        = envOr("ECK_VERSION", "3.5.0")
	elasticVersion    = envOr("ELASTIC_STACK_VERSION", "9.5.4")
	rabbitmqVersion   = envOr("RABBITMQ_OPERATOR_VERSION", "v2.23.0")
	clickhouseVersion = envOr("CLICKHOUSE_OPERATOR_VERSION", "release-0.27.3")
	keycloakVersion   = envOr("KEYCLOAK_VERSION", "26.7.4")
	starrocksVersion  = envOr("STARROCKS_OPERATOR_VERSION", "v1.11.7")
	mariadbChartVer   = envOr("MARIADB_OPERATOR_VERSION", "26.10.1")
	redisChartVersion = envOr("REDIS_OPERATOR_VERSION", "0.26.1")
	// The upstream example tracks :latest. This is pinned so a new upstream build
	// cannot change what the test means.
	redisImageTag = envOr("REDIS_IMAGE_TAG", "v8.10.1")

	kafkaVersion = envOr("KAFKA_VERSION", "4.3.1")

	// strimziAPIVersion tracks the operator generation: Strimzi 1.x serves only "v1",
	// while the 0.4x line served "v1beta2". Override when testing against 0.4x.
	strimziAPIVersion = envOr("STRIMZI_API_VERSION", "v1")

	// StarRocks ships no image defaults on the CRD itself. Only its Helm chart
	// supplies them, so a bare StarRocksCluster must name them explicitly.
	starrocksImageTag = envOr("STARROCKS_IMAGE_TAG", "4.1-latest")
	// SeaweedFS backs the shared-data StarRocks case with an S3-compatible endpoint.
	seaweedfsImageTag = envOr("SEAWEEDFS_IMAGE_TAG", "4.47")
)

// keycloakKustomization is the cluster-wide install target, applied with `kubectl -k`.
//
// Two reasons it has to go through kustomize rather than kubectl alone. The default
// manifest under kubernetes/ sets JOSDK_WATCH_CURRENT, so the operator would watch
// only its own namespace. The cluster-wide variant sets JOSDK_ALL_NAMESPACES. Its
// ClusterRoleBindings ship with no subject namespace at all — the kustomization's
// NamespaceTransformer fills them in, which `kubectl apply -n` cannot do, so applying
// the raw YAML is rejected with "subjects[0].namespace: Required value".
//
// The kustomization also pulls in all four CRDs, so they need no separate step.
func keycloakKustomization() string {
	return fmt.Sprintf(
		"github.com/keycloak/keycloak-k8s-resources/kubernetes/cluster-wide?ref=%s",
		keycloakVersion)
}

// seaweedfsManifest deploys an S3-compatible object store into the workload namespace,
// standing in for the external bucket a shared-data StarRocks cluster would really use.
// The %s is the namespace.
func seaweedfsManifest() string {
	return fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seaweedfs
  namespace: %%[1]s
  labels:
    app: seaweedfs
    lightsout-e2e/role: storage
spec:
  replicas: 1
  selector:
    matchLabels:
      app: seaweedfs
  template:
    metadata:
      labels:
        app: seaweedfs
        lightsout-e2e/role: storage
    spec:
      containers:
        - name: seaweedfs
          image: chrislusf/seaweedfs:%[1]s
          args: ["server", "-dir=/data", "-s3", "-s3.port=8333", "-master.volumeSizeLimitMB=256"]
          ports:
            - {name: s3, containerPort: 8333}
            - {name: master, containerPort: 9333}
          resources:
            requests:
              cpu: 50m
              memory: 256Mi
          volumeMounts:
            - {name: data, mountPath: /data}
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: seaweedfs
  namespace: %%[1]s
  labels:
    lightsout-e2e/role: storage
spec:
  selector:
    app: seaweedfs
  ports:
    - {name: s3, port: 8333, targetPort: 8333}
    - {name: master, port: 9333, targetPort: 9333}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: seaweedfs-bucket
  namespace: %%[1]s
  labels:
    lightsout-e2e/role: storage
spec:
  backoffLimit: 10
  template:
    metadata:
      labels:
        lightsout-e2e/role: storage
    spec:
      restartPolicy: OnFailure
      containers:
        - name: create-bucket
          image: curlimages/curl:8.11.1
          command: ["sh", "-c"]
          args:
            - |
              until curl -sf -X PUT http://seaweedfs:8333/starrocks; do
                echo "waiting for the seaweedfs s3 gateway..."
                sleep 5
              done
              echo "bucket created"
`, seaweedfsImageTag)
}

// starrocksSharedDataManifest builds a StarRocksCluster in shared-data mode: FE plus CN,
// no BE, with table data in object storage rather than on local disks. The FE config has
// to come through a ConfigMap because run_mode is an fe.conf setting, not a CRD field.
// The %s is the namespace.
func starrocksSharedDataManifest() string {
	return fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: srs-fe-cm
  namespace: %%[1]s
data:
  fe.conf: |
    LOG_DIR = ${STARROCKS_HOME}/log
    JAVA_OPTS="-Dlog4j2.formatMsgNoLookups=true -Xmx4096m -XX:+UseG1GC"
    http_port = 8030
    rpc_port = 9020
    query_port = 9030
    edit_log_port = 9010
    mysql_service_nio_enabled = true
    sys_log_level = INFO
    run_mode = shared_data
    cloud_native_meta_port = 6090
    enable_load_volume_from_conf = true
    cloud_native_storage_type = S3
    aws_s3_path = starrocks/data
    aws_s3_endpoint = seaweedfs:8333
    aws_s3_region = us-east-1
    aws_s3_access_key = lightsout
    aws_s3_secret_key = lightsout
    aws_s3_use_aws_sdk_default_behavior = false
    aws_s3_use_instance_profile = false
    aws_s3_enable_path_style_access = true
    aws_s3_enable_ssl = false
---
apiVersion: starrocks.com/v1
kind: StarRocksCluster
metadata:
  name: srs
  namespace: %%[1]s
spec:
  starRocksFeSpec:
    replicas: 1
    image: starrocks/fe-ubuntu:%[1]s
    configMapInfo:
      configMapName: srs-fe-cm
      resolveKey: fe.conf
    requests:
      cpu: 100m
      memory: 2Gi
  starRocksCnSpec:
    replicas: 1
    image: starrocks/cn-ubuntu:%[1]s
    requests:
      cpu: 100m
      memory: 2Gi
`, starrocksImageTag)
}

// starrocksCRD builds the URL for one of the StarRocks CRD release assets.
func starrocksCRD(name string) string {
	return fmt.Sprintf(
		"https://github.com/StarRocks/starrocks-kubernetes-operator/releases/download/%s/starrocks.com_%s.yaml",
		starrocksVersion, name)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// operatorCase is one operator's full round trip: install it, give it something to
// provision, turn that off through a schedule, and turn it back on.
type operatorCase struct {
	// Key selects this case through the OPERATORS environment variable.
	Key string
	// Name is what shows up in test output.
	Name string
	// Footprint is a short note about what this case costs to run, surfaced when
	// the case is skipped so the reader knows what they are opting into.
	Footprint string

	// OperatorNamespace is where the operator itself runs, kept separate from the
	// namespace holding its data plane. That mirrors how these are deployed for real
	// — platform components in their own namespace, workloads in tenant namespaces —
	// and it keeps the operator out of the schedule's scope, so the scaler can never
	// take the operator down alongside what it manages.
	OperatorNamespace string

	// Install runs before anything else. Each entry is a full argv. "{{NS}}" is
	// replaced with the workload namespace and "{{OPNS}}" with OperatorNamespace.
	Install [][]string
	// Uninstall runs last and is best-effort: failures are warnings, not errors.
	Uninstall [][]string
	// OperatorReady waits for the operator's own workload, as a kubectl argv.
	OperatorReady []string
	// CRDs must be Established before the custom resource is created.
	CRDs []string

	// RBACRules are the apiGroups/resources lightsout needs for this operator,
	// granted to its ServiceAccount for the duration of the case. These mirror the
	// rbac.customResources Helm value.
	RBACRules []rbacRule

	// Prerequisite is a manifest applied before Resource, with a single %s for the
	// namespace. For backing services the custom resource depends on — object storage,
	// for instance — which have to be up before the operator can provision anything.
	Prerequisite string
	// PrerequisiteReady waits for that manifest, as a kubectl argv.
	PrerequisiteReady []string

	// ExcludeLabels keeps the schedule off workloads it must not touch, rendered into
	// the schedule's spec.excludeLabels. Needed when a prerequisite runs in the
	// workload namespace: it has no controller owner reference, so the scaler would
	// otherwise take it down alongside the data plane it is backing.
	ExcludeLabels map[string]string

	// Resource is the custom resource manifest, with a single %s for the namespace.
	Resource string
	// PodSelector matches the data-plane pods the operator provisions.
	PodSelector string
	// ExpectedPods is how many of those pods exist while the schedule is Up.
	ExpectedPods int
	// ProvisionTimeout overrides how long to wait for the operator to bring its data
	// plane up, for cases whose images are large enough that the default is too tight.
	ProvisionTimeout time.Duration
	// RequireReady counts only pods reporting Ready, rather than any pod object that
	// exists. Without it a pod stuck Pending — unschedulable for want of memory, say —
	// satisfies the assertion, so the test proves the operator reacted to the restored
	// spec without proving the service actually came back.
	//
	// Off for cases whose pods legitimately never reach Ready in this environment.
	RequireReady bool

	// CustomResources is the schedule's spec.customResources block, already
	// indented to sit under "spec:".
	CustomResources string
	// IncludeOwnedWorkloads is set for operators that only offer a pause switch,
	// leaving lightsout to scale the workloads itself.
	IncludeOwnedWorkloads bool

	// Assertions on the custom resource itself, so a passing test proves the field
	// was written rather than only that pods disappeared.
	CRRef        string // "kind.group/name" for kubectl
	FieldPath    string // jsonpath into the custom resource
	FieldWhenUp  string // expected value while Up. An empty string means the field is absent
	FieldWhenOff string // expected value while Down
}

type rbacRule struct {
	APIGroups []string
	Resources []string
	Verbs     []string
}

// operatorCases is the full matrix. Every entry corresponds to a recipe in
// docs/custom-resources.md.
var operatorCases = []operatorCase{
	{
		Key:       "rabbitmq",
		Name:      "RabbitMQ",
		Footprint: "1 RabbitMQ pod, ~250Mi. The lightest case. Start here.",
		// cluster-operator.yml creates rabbitmq-system and pins the operator there.
		OperatorNamespace: "rabbitmq-system",
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://github.com/rabbitmq/cluster-operator/releases/download/%s/cluster-operator.yml", rabbitmqVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				fmt.Sprintf("https://github.com/rabbitmq/cluster-operator/releases/download/%s/cluster-operator.yml", rabbitmqVersion)},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/rabbitmq-cluster-operator",
			"--for=condition=Available", "-n", "rabbitmq-system", "--timeout=5m"},
		CRDs: []string{"rabbitmqclusters.rabbitmq.com"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"rabbitmq.com"}, Resources: []string{"rabbitmqclusters"}},
		},
		Resource: `
apiVersion: rabbitmq.com/v1beta1
kind: RabbitmqCluster
metadata:
  name: rmq
  namespace: %s
spec:
  replicas: 1
  persistence:
    storage: 256Mi
  resources:
    requests:
      memory: 256Mi
`,
		PodSelector:  "app.kubernetes.io/name=rmq",
		ExpectedPods: 1,
		// RabbitMQ reports Ready once the node finishes booting.
		RequireReady: true,
		CustomResources: `
  customResources:
    - group: rabbitmq.com
      version: v1beta1
      kind: RabbitmqCluster
      setFields:
        - path: /spec/replicas
          value: 0`,
		CRRef:        "rabbitmqcluster.rabbitmq.com/rmq",
		FieldPath:    "{.spec.replicas}",
		FieldWhenUp:  "1",
		FieldWhenOff: "0",
	},
	{
		Key:       "clickhouse",
		Name:      "ClickHouse (Altinity)",
		Footprint: "1 ClickHouse pod, ~500Mi.",
		// The install bundle hardcodes kube-system throughout. Relocating it needs the
		// template variant plus envsubst, so the bundle stays as upstream ships it.
		OperatorNamespace: "kube-system",
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://raw.githubusercontent.com/Altinity/clickhouse-operator/%s/deploy/operator/clickhouse-operator-install-bundle.yaml", clickhouseVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				fmt.Sprintf("https://raw.githubusercontent.com/Altinity/clickhouse-operator/%s/deploy/operator/clickhouse-operator-install-bundle.yaml", clickhouseVersion)},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/clickhouse-operator",
			"--for=condition=Available", "-n", "kube-system", "--timeout=5m"},
		CRDs: []string{"clickhouseinstallations.clickhouse.altinity.com"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"clickhouse.altinity.com"}, Resources: []string{"clickhouseinstallations"}},
		},
		Resource: `
apiVersion: clickhouse.altinity.com/v1
kind: ClickHouseInstallation
metadata:
  name: ch
  namespace: %s
spec:
  configuration:
    clusters:
      - name: single
        layout:
          shardsCount: 1
          replicasCount: 1
`,
		PodSelector:  "clickhouse.altinity.com/chi=ch",
		ExpectedPods: 1,
		// ClickHouse reports Ready once it answers on the HTTP port.
		RequireReady: true,
		// spec.stop is a purpose-built hibernate: the operator zeroes each StatefulSet
		// and keeps every PVC. The field is a string-bool, so "yes" rather than true.
		CustomResources: `
  customResources:
    - group: clickhouse.altinity.com
      version: v1
      kind: ClickHouseInstallation
      setFields:
        - path: /spec/stop
          value: "yes"`,
		CRRef:        "clickhouseinstallation.clickhouse.altinity.com/ch",
		FieldPath:    "{.spec.stop}",
		FieldWhenUp:  "",
		FieldWhenOff: "yes",
	},
	{
		Key:       "cnpg",
		Name:      "CloudNativePG",
		Footprint: "1 Postgres pod, ~300Mi. The lightest case. Start here.",
		// The release manifest pins the operator to cnpg-system and creates it.
		OperatorNamespace: "cnpg-system",
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%s/cnpg-%s.yaml", cnpgVersion, cnpgVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%s/cnpg-%s.yaml", cnpgVersion, cnpgVersion)},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/cnpg-controller-manager",
			"--for=condition=Available", "-n", "cnpg-system", "--timeout=5m"},
		CRDs: []string{"clusters.postgresql.cnpg.io"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"postgresql.cnpg.io"}, Resources: []string{"clusters"}},
		},
		Resource: `
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: pg
  namespace: %s
spec:
  instances: 1
  storage:
    size: 256Mi
`,
		PodSelector:  "cnpg.io/cluster=pg",
		ExpectedPods: 1,
		// Postgres reports Ready once the instance is up.
		RequireReady: true,
		CustomResources: `
  customResources:
    - group: postgresql.cnpg.io
      version: v1
      kind: Cluster
      setFields:
        - path: /metadata/annotations/cnpg.io~1hibernation
          value: "on"`,
		CRRef:        "cluster.postgresql.cnpg.io/pg",
		FieldPath:    `{.metadata.annotations.cnpg\.io/hibernation}`,
		FieldWhenUp:  "",
		FieldWhenOff: "on",
	},
	{
		Key:       "eck",
		Name:      "ECK (Elasticsearch)",
		Footprint: "1 Elasticsearch pod, ~2Gi. The manifest disables mmap for Kind.",
		// operator.yaml pins the operator to elastic-system and creates it.
		OperatorNamespace: "elastic-system",
		Install: [][]string{
			{"kubectl", "create", "-f", fmt.Sprintf("https://download.elastic.co/downloads/eck/%s/crds.yaml", eckVersion)},
			{"kubectl", "apply", "-f", fmt.Sprintf("https://download.elastic.co/downloads/eck/%s/operator.yaml", eckVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f", fmt.Sprintf("https://download.elastic.co/downloads/eck/%s/operator.yaml", eckVersion)},
			{"kubectl", "delete", "--ignore-not-found", "-f", fmt.Sprintf("https://download.elastic.co/downloads/eck/%s/crds.yaml", eckVersion)},
		},
		OperatorReady: []string{"kubectl", "rollout", "status", "statefulset/elastic-operator",
			"-n", "elastic-system", "--timeout=5m"},
		CRDs: []string{"elasticsearches.elasticsearch.k8s.elastic.co"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"elasticsearch.k8s.elastic.co"}, Resources: []string{"elasticsearches"}},
		},
		Resource: fmt.Sprintf(`
apiVersion: elasticsearch.k8s.elastic.co/v1
kind: Elasticsearch
metadata:
  name: es
  namespace: %%s
spec:
  version: %s
  nodeSets:
    - name: default
      count: 1
      config:
        node.store.allow_mmap: false
      podTemplate:
        spec:
          containers:
            - name: elasticsearch
              resources:
                requests:
                  memory: 1Gi
`, elasticVersion),
		PodSelector:  "elasticsearch.k8s.elastic.co/cluster-name=es",
		ExpectedPods: 1,
		// The earlier dump showed health: green and availableNodes: 1.
		RequireReady: true,
		// Zeroing nodeSets is not possible: ECK's webhook requires at least one master
		// nodeSet with count > 0, so the last one can never be taken to zero. The
		// supported route is to pause orchestration and let the scaler zero the
		// StatefulSets ECK owns.
		CustomResources: `
  customResources:
    - group: elasticsearch.k8s.elastic.co
      version: v1
      kind: Elasticsearch
      setFields:
        - path: /metadata/annotations/eck.k8s.elastic.co~1pause-orchestration
          value: "true"`,
		IncludeOwnedWorkloads: true,
		CRRef:                 "elasticsearch.elasticsearch.k8s.elastic.co/es",
		FieldPath:             `{.metadata.annotations.eck\.k8s\.elastic\.co/pause-orchestration}`,
		FieldWhenUp:           "",
		FieldWhenOff:          "true",
	},
	{
		Key:       "keycloak",
		Name:      "Keycloak",
		Footprint: "1 Keycloak pod, ~700Mi. Uses the dev-file database. The pod may never report Ready, so this case counts pods rather than readiness.",
		// The cluster-wide kustomization sets `namespace: keycloak-operator`, so that is
		// where the operator and its ServiceAccount land.
		OperatorNamespace: "keycloak-operator",
		// All four CRDs are required: the operator starts informers for every kind it
		// owns and crash-loops if any of them is absent. The upstream kustomization
		// lists exactly this set.
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-k", keycloakKustomization()},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-k", keycloakKustomization()},
			{"kubectl", "delete", "ns", "{{OPNS}}", "--ignore-not-found", "--timeout=5m"},
		},
		OperatorReady: []string{"kubectl", "rollout", "status", "deployment/keycloak-operator",
			"-n", "{{OPNS}}", "--timeout=5m"},
		CRDs: []string{"keycloaks.k8s.keycloak.org"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"k8s.keycloak.org"}, Resources: []string{"keycloaks"}},
		},
		Resource: `
apiVersion: k8s.keycloak.org/v2beta1
kind: Keycloak
metadata:
  name: kc
  namespace: %s
spec:
  instances: 1
  db:
    vendor: dev-file
  http:
    httpEnabled: true
  hostname:
    strict: false
`,
		PodSelector:  "app=keycloak",
		ExpectedPods: 1,
		CustomResources: `
  customResources:
    - group: k8s.keycloak.org
      version: v2beta1
      kind: Keycloak
      setFields:
        - path: /spec/instances
          value: 0`,
		CRRef:        "keycloak.k8s.keycloak.org/kc",
		FieldPath:    "{.spec.instances}",
		FieldWhenUp:  "1",
		FieldWhenOff: "0",
	},
	{
		Key:               "mariadb",
		Name:              "MariaDB",
		Footprint:         "1 MariaDB pod, ~500Mi. Needs Helm. Uses the pause-plus-scale path.",
		OperatorNamespace: "mariadb-operator",
		// The project's README now publishes the charts to its OCI registry rather than
		// the classic Helm repo, so that is what this installs from.
		Install: [][]string{
			{"helm", "install", "mariadb-operator-crds", "oci://ghcr.io/mariadb-operator/charts/mariadb-operator-crds",
				"--version", mariadbChartVer, "-n", "{{OPNS}}"},
			{"helm", "install", "mariadb-operator", "oci://ghcr.io/mariadb-operator/charts/mariadb-operator",
				"--version", mariadbChartVer, "-n", "{{OPNS}}", "--wait", "--timeout", "5m"},
		},
		Uninstall: [][]string{
			{"helm", "uninstall", "mariadb-operator", "-n", "{{OPNS}}"},
			{"helm", "uninstall", "mariadb-operator-crds", "-n", "{{OPNS}}"},
			{"kubectl", "delete", "ns", "{{OPNS}}", "--ignore-not-found", "--timeout=5m"},
		},
		// Release name and chart name match, so the chart's fullname template collapses
		// to plain "mariadb-operator" rather than doubling it. `helm install --wait`
		// above has already waited for the webhook and cert-controller deployments.
		OperatorReady: []string{"kubectl", "wait", "deployment/mariadb-operator",
			"--for=condition=Available", "-n", "{{OPNS}}", "--timeout=5m"},
		CRDs: []string{"mariadbs.k8s.mariadb.com"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"k8s.mariadb.com"}, Resources: []string{"mariadbs"}},
		},
		Resource: `
apiVersion: k8s.mariadb.com/v1alpha1
kind: MariaDB
metadata:
  name: mdb
  namespace: %s
spec:
  rootPasswordSecretKeyRef:
    name: mdb-root
    key: password
    generate: true
  storage:
    size: 256Mi
`,
		PodSelector:  "app.kubernetes.io/instance=mdb",
		ExpectedPods: 1,
		// MariaDB reports Ready once the server accepts connections.
		RequireReady: true,
		CustomResources: `
  customResources:
    - group: k8s.mariadb.com
      version: v1alpha1
      kind: MariaDB
      setFields:
        - path: /spec/suspend
          value: true`,
		IncludeOwnedWorkloads: true,
		CRRef:                 "mariadb.k8s.mariadb.com/mdb",
		FieldPath:             "{.spec.suspend}",
		FieldWhenUp:           "false",
		FieldWhenOff:          "true",
	},
	{
		Key:               "redis",
		Name:              "Redis (OpsTree)",
		Footprint:         "1 Redis pod, ~150Mi. Needs Helm. Standalone Redis is the lightest kind. The skip-reconcile annotation key differs per kind.",
		OperatorNamespace: "redis-operator",
		Install: [][]string{
			{"helm", "repo", "add", "ot-helm", "https://ot-container-kit.github.io/helm-charts/"},
			{"helm", "repo", "update", "ot-helm"},
			{"helm", "install", "redis-operator", "ot-helm/redis-operator",
				"--version", redisChartVersion, "-n", "{{OPNS}}", "--wait", "--timeout", "5m"},
		},
		Uninstall: [][]string{
			{"helm", "uninstall", "redis-operator", "-n", "{{OPNS}}"},
			{"kubectl", "delete", "ns", "{{OPNS}}", "--ignore-not-found", "--timeout=5m"},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/redis-operator",
			"--for=condition=Available", "-n", "{{OPNS}}", "--timeout=5m"},
		CRDs: []string{"redis.redis.redis.opstreelabs.in"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"redis.redis.opstreelabs.in"}, Resources: []string{"redis", "redisclusters", "redisreplications", "redissentinels"}},
		},
		Resource: fmt.Sprintf(`
apiVersion: redis.redis.opstreelabs.in/v1beta2
kind: Redis
metadata:
  name: redis
  namespace: %%s
spec:
  kubernetesConfig:
    image: quay.io/opstree/redis:%s
    imagePullPolicy: IfNotPresent
`, redisImageTag),
		PodSelector:  "app=redis",
		ExpectedPods: 1,
		// Standalone Redis reports Ready once it answers PING.
		RequireReady: true,
		CustomResources: `
  customResources:
    - group: redis.redis.opstreelabs.in
      version: v1beta2
      kind: Redis
      setFields:
        - path: /metadata/annotations/redis.opstreelabs.in~1skip-reconcile
          value: "true"`,
		IncludeOwnedWorkloads: true,
		CRRef:                 "redis.redis.redis.opstreelabs.in/redis",
		FieldPath:             `{.metadata.annotations.redis\.opstreelabs\.in/skip-reconcile}`,
		FieldWhenUp:           "",
		FieldWhenOff:          "true",
	},
	{
		Key:       "strimzi",
		Name:      "Strimzi (Kafka)",
		Footprint: "1 combined broker/controller pod, ~1.5Gi. The only case that uses `delete: true`.",
		// Strimzi enforces no namespace of its own, and its quickstart co-locates the
		// operator and the cluster in "kafka". These steps split them, which is the
		// central-operator layout a platform team would run.
		//
		// The cluster operator defaults STRIMZI_NAMESPACE to its own metadata.namespace,
		// so it watches only where it runs. Running it centrally means installing it in
		// its own namespace, repointing STRIMZI_NAMESPACE at the watched namespaces, and
		// granting RoleBindings in each.
		OperatorNamespace: "strimzi-system",
		Install: [][]string{
			{"kubectl", "create", "-f", "https://strimzi.io/install/latest?namespace=" + operatorNamespaceToken,
				"-n", operatorNamespaceToken},
			{"kubectl", "-n", operatorNamespaceToken, "set", "env", "deployment/strimzi-cluster-operator",
				"STRIMZI_NAMESPACE=" + namespaceToken},
			// The operator needs these in every namespace it watches. Its install creates
			// them only in its own namespace.
			{"kubectl", "create", "rolebinding", "strimzi-cluster-operator", "-n", namespaceToken,
				"--clusterrole=strimzi-cluster-operator-namespaced",
				"--serviceaccount=" + operatorNamespaceToken + ":strimzi-cluster-operator"},
			{"kubectl", "create", "rolebinding", "strimzi-cluster-operator-watched", "-n", namespaceToken,
				"--clusterrole=strimzi-cluster-operator-watched",
				"--serviceaccount=" + operatorNamespaceToken + ":strimzi-cluster-operator"},
			{"kubectl", "create", "rolebinding", "strimzi-cluster-operator-entity-operator-delegation",
				"-n", namespaceToken, "--clusterrole=strimzi-entity-operator",
				"--serviceaccount=" + operatorNamespaceToken + ":strimzi-cluster-operator"},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				"https://strimzi.io/install/latest?namespace=" + operatorNamespaceToken, "-n", operatorNamespaceToken},
			{"kubectl", "delete", "ns", operatorNamespaceToken, "--ignore-not-found", "--timeout=5m"},
		},
		OperatorReady: []string{"kubectl", "rollout", "status", "deployment/strimzi-cluster-operator",
			"-n", operatorNamespaceToken, "--timeout=5m"},
		CRDs: []string{"kafkas.kafka.strimzi.io", "strimzipodsets.core.strimzi.io"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"kafka.strimzi.io"}, Resources: []string{"kafkas", "kafkanodepools"}},
			{
				APIGroups: []string{"core.strimzi.io"},
				Resources: []string{"strimzipodsets"},
				Verbs:     []string{"get", "list", "watch", "delete"},
			},
		},
		// Strimzi 1.x serves only kafka.strimzi.io/v1. It removed v1beta2. Node pools
		// and KRaft are mandatory there, so the enabling annotations the 0.4x line
		// needed are gone too, and spec.kafka no longer carries replicas or storage.
		Resource: fmt.Sprintf(`
apiVersion: %[1]s
kind: KafkaNodePool
metadata:
  name: dual
  namespace: %%[1]s
  labels:
    strimzi.io/cluster: kafka
spec:
  replicas: 1
  roles:
    - controller
    - broker
  storage:
    type: ephemeral
---
apiVersion: %[1]s
kind: Kafka
metadata:
  name: kafka
  namespace: %%[1]s
spec:
  kafka:
    version: %[2]s
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
    config:
      offsets.topic.replication.factor: 1
      transaction.state.log.replication.factor: 1
      transaction.state.log.min.isr: 1
      default.replication.factor: 1
      min.insync.replicas: 1
`, "kafka.strimzi.io/"+strimziAPIVersion, kafkaVersion),
		PodSelector:  "strimzi.io/cluster=kafka",
		ExpectedPods: 1,
		// The broker reports Ready once it has joined the KRaft quorum.
		RequireReady: true,
		CustomResources: fmt.Sprintf(`
  customResources:
    - group: kafka.strimzi.io
      version: %[1]s
      kind: Kafka
      setFields:
        - path: /metadata/annotations/strimzi.io~1pause-reconciliation
          value: "true"
    - group: core.strimzi.io
      version: %[1]s
      kind: StrimziPodSet
      delete: true`, strimziAPIVersion),
		CRRef:        "kafka.kafka.strimzi.io/kafka",
		FieldPath:    `{.metadata.annotations.strimzi\.io/pause-reconciliation}`,
		FieldWhenUp:  "",
		FieldWhenOff: "true",
	},
	{
		Key:       "starrocks",
		Name:      "StarRocks (shared-nothing)",
		Footprint: "1 FE + 1 BE pod, ~6Gi. Classic mode, data on local disks. Checks that FE returns from zero replicas.",
		// operator.yaml pins the operator to the starrocks namespace and creates it.
		OperatorNamespace: "starrocks",
		// operator.yaml carries no CRDs. They ship as separate release assets. Without
		// them the operator starts and then fails its informer with
		// `no matches for kind "StarRocksCluster"`.
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-f", starrocksCRD("starrocksclusters")},
			{"kubectl", "apply", "--server-side", "-f", starrocksCRD("starrockswarehouses")},
			{"kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://github.com/StarRocks/starrocks-kubernetes-operator/releases/download/%s/operator.yaml", starrocksVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				fmt.Sprintf("https://github.com/StarRocks/starrocks-kubernetes-operator/releases/download/%s/operator.yaml", starrocksVersion)},
			{"kubectl", "delete", "--ignore-not-found", "-f", starrocksCRD("starrocksclusters")},
			{"kubectl", "delete", "--ignore-not-found", "-f", starrocksCRD("starrockswarehouses")},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/kube-starrocks-operator",
			"--for=condition=Available", "-n", "starrocks", "--timeout=10m"},
		CRDs: []string{"starrocksclusters.starrocks.com"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"starrocks.com"}, Resources: []string{"starrocksclusters"}},
		},
		Resource: fmt.Sprintf(`
apiVersion: starrocks.com/v1
kind: StarRocksCluster
metadata:
  name: sr
  namespace: %%s
spec:
  starRocksFeSpec:
    replicas: 1
    image: starrocks/fe-ubuntu:%[1]s
    requests:
      cpu: 100m
      memory: 2Gi
  starRocksBeSpec:
    replicas: 1
    image: starrocks/be-ubuntu:%[1]s
    requests:
      cpu: 100m
      memory: 2Gi
`, starrocksImageTag),
		PodSelector:  "app.starrocks.ownerreference/name=sr-fe",
		ExpectedPods: 1,
		// Two multi-GB image pulls before anything starts.
		ProvisionTimeout: 25 * time.Minute,
		CustomResources: `
  customResources:
    - group: starrocks.com
      version: v1
      kind: StarRocksCluster
      setFields:
        - path: /spec/starRocksBeSpec/replicas
          value: 0
        - path: /spec/starRocksFeSpec/replicas
          value: 0`,
		CRRef:        "starrockscluster.starrocks.com/sr",
		FieldPath:    "{.spec.starRocksFeSpec.replicas}",
		FieldWhenUp:  "1",
		FieldWhenOff: "0",
	},
	{
		Key:  "starrocks-shared",
		Name: "StarRocks (shared-data)",
		Footprint: "1 FE + 1 CN pod plus SeaweedFS, ~6Gi and three image pulls. Storage-compute " +
			"separation. Data lives in object storage, so there is no BE and the compute tier is CN.",
		// Same operator as the shared-nothing case. Only the cluster shape differs.
		OperatorNamespace: "starrocks",
		Install: [][]string{
			{"kubectl", "apply", "--server-side", "-f", starrocksCRD("starrocksclusters")},
			{"kubectl", "apply", "--server-side", "-f", starrocksCRD("starrockswarehouses")},
			{"kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://github.com/StarRocks/starrocks-kubernetes-operator/releases/download/%s/operator.yaml", starrocksVersion)},
		},
		Uninstall: [][]string{
			{"kubectl", "delete", "--ignore-not-found", "-f",
				fmt.Sprintf("https://github.com/StarRocks/starrocks-kubernetes-operator/releases/download/%s/operator.yaml", starrocksVersion)},
			{"kubectl", "delete", "--ignore-not-found", "-f", starrocksCRD("starrocksclusters")},
			{"kubectl", "delete", "--ignore-not-found", "-f", starrocksCRD("starrockswarehouses")},
		},
		OperatorReady: []string{"kubectl", "wait", "deployment/kube-starrocks-operator",
			"--for=condition=Available", "-n", operatorNamespaceToken, "--timeout=10m"},
		CRDs: []string{"starrocksclusters.starrocks.com"},
		RBACRules: []rbacRule{
			{APIGroups: []string{"starrocks.com"}, Resources: []string{"starrocksclusters"}},
		},

		// SeaweedFS stands in for S3. It runs all-in-one (master, volume, filer and S3
		// gateway in one process) with no S3 credential config, which leaves the gateway
		// unauthenticated, so the dummy keys in fe.conf are accepted. The Job creates the
		// bucket up front because StarRocks expects it to exist already.
		Prerequisite: seaweedfsManifest(),
		PrerequisiteReady: []string{"kubectl", "wait", "--for=condition=Complete",
			"job/seaweedfs-bucket", "-n", namespaceToken, "--timeout=5m"},

		// Without this the scaler would take SeaweedFS down alongside the data plane: it
		// is a plain Deployment with no controller owner reference. In a real deployment
		// the object store is external and the question does not arise.
		ExcludeLabels: map[string]string{"lightsout-e2e/role": "storage"},

		Resource:     starrocksSharedDataManifest(),
		PodSelector:  "app.starrocks.ownerreference/name=srs-fe",
		ExpectedPods: 1,
		// Three images to pull, then a shared-data bootstrap against object storage.
		ProvisionTimeout: 25 * time.Minute,
		CustomResources: `
  customResources:
    - group: starrocks.com
      version: v1
      kind: StarRocksCluster
      setFields:
        - path: /spec/starRocksCnSpec/replicas
          value: 0
        - path: /spec/starRocksFeSpec/replicas
          value: 0`,
		CRRef:        "starrockscluster.starrocks.com/srs",
		FieldPath:    "{.spec.starRocksFeSpec.replicas}",
		FieldWhenUp:  "1",
		FieldWhenOff: "0",
	},
}

// selectedOperators reads the OPERATORS environment variable. "all" runs every
// case. A comma-separated list runs those. An unset value runs none.
func selectedOperators() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("OPERATORS"))
	if raw == "" {
		return nil
	}
	if raw == "all" {
		out := make(map[string]bool, len(operatorCases))
		for _, c := range operatorCases {
			out[c.Key] = true
		}
		return out
	}
	out := map[string]bool{}
	for _, key := range strings.Split(raw, ",") {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = true
		}
	}
	return out
}

// Serial only at this level: each case is its own Ordered container below, so a
// failure in one case's setup cannot poison the others.
var _ = Describe("Operator Integration", Serial, func() {
	selected := selectedOperators()

	for i := range operatorCases {
		tc := operatorCases[i]

		// Ordered per case, not across cases. BeforeAll and AfterAll attach to the
		// nearest Ordered container, so without this every case's setup would hang off
		// one shared container and a single failed install would skip everything after
		// it. ContinueOnFailure then lets the remaining specs inside this case run.
		Context(tc.Name, Ordered, ContinueOnFailure, func() {
			testNamespace := "lightsout-op-" + tc.Key
			scheduleName := "op-" + tc.Key
			rbacName := "lightsout-e2e-" + tc.Key

			BeforeAll(func() {
				if !selected[tc.Key] {
					Skip(fmt.Sprintf("not selected (OPERATOR=%s)", tc.Key))
				}

				By("granting lightsout access to the operator's custom resources")
				applyOperatorRBAC(rbacName, tc.RBACRules)

				// Both namespaces exist before the install, because a namespace-scoped
				// operator needs its watched namespace to already be there: Strimzi's
				// RoleBindings are created in the watched namespace at install time.
				// Failures are ignored — a manifest that creates its own namespace, or a
				// leftover from a previous run, is fine either way.
				By("creating the operator and workload namespaces")
				for _, ns := range []string{tc.OperatorNamespace, testNamespace} {
					cmd := exec.Command("kubectl", "create", "ns", ns)
					_, _ = utils.Run(cmd)
				}

				By("installing " + tc.Name)
				for _, step := range tc.Install {
					step = withNamespaces(step, testNamespace, tc.OperatorNamespace)
					cmd := exec.Command(step[0], step[1:]...)
					out, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred(), "install step failed: %s\n%s", strings.Join(step, " "), out)
				}

				By("waiting for the operator to become available")
				ready := withNamespaces(tc.OperatorReady, testNamespace, tc.OperatorNamespace)
				cmd := exec.Command(ready[0], ready[1:]...)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "operator did not become available")

				By("waiting for the operator's CRDs to be established")
				for _, crd := range tc.CRDs {
					cmd := exec.Command("kubectl", "wait", "--for=condition=Established",
						"crd/"+crd, "--timeout=2m")
					_, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred(), "CRD %s was not established", crd)
				}
			})

			AfterEach(func() {
				if !selected[tc.Key] || !CurrentSpecReport().Failed() {
					return
				}
				By("collecting namespace state and controller logs")
				dumpDiagnostics(tc, testNamespace)
			})

			AfterAll(func() {
				if !selected[tc.Key] {
					return
				}

				By("deleting the schedule")
				cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName,
					"-n", namespace, "--ignore-not-found", "--timeout=2m")
				_, _ = utils.Run(cmd)

				// Delete the custom resource while its operator is still installed, so any
				// finalizers it carries can actually run.
				if tc.CRRef != "" {
					By("deleting the custom resource")
					cmd = exec.Command("kubectl", "delete", tc.CRRef, "-n", testNamespace,
						"--ignore-not-found", "--timeout=90s")
					_, _ = utils.Run(cmd)
				}

				// Do not block on the namespace going away. Waiting for finalizers and PVCs
				// here cost ~450s across the suite and verified nothing. The next case uses
				// its own namespace, and cleanup-test-e2e deletes the cluster.
				By("deleting the test namespace (not waiting)")
				cmd = exec.Command("kubectl", "delete", "ns", testNamespace,
					"--ignore-not-found", "--wait=false")
				_, _ = utils.Run(cmd)

				By("revoking the operator RBAC")
				for _, kind := range []string{"clusterrolebinding", "clusterrole"} {
					cmd = exec.Command("kubectl", "delete", kind, rbacName, "--ignore-not-found")
					_, _ = utils.Run(cmd)
				}

				By("uninstalling " + tc.Name)
				for _, step := range tc.Uninstall {
					step = withNamespaces(step, testNamespace, tc.OperatorNamespace)
					cmd := exec.Command(step[0], step[1:]...)
					if out, err := utils.Run(cmd); err != nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "warning: uninstall step failed: %s\n%s\n",
							strings.Join(step, " "), out)
					}
				}
			})

			It("provisions its workloads, hibernates them on downscale, and restores them on upscale", func() {
				if tc.Prerequisite != "" {
					By("creating the backing services")
					applyManifest(fmt.Sprintf(tc.Prerequisite, testNamespace), tc.Key+"-prereq")

					By("waiting for the prerequisite to be ready")
					ready := withNamespaces(tc.PrerequisiteReady, testNamespace, tc.OperatorNamespace)
					cmd := exec.Command(ready[0], ready[1:]...)
					out, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred(), "prerequisite never became ready:\n%s", out)
				}

				By("creating the custom resource")
				applyManifest(fmt.Sprintf(tc.Resource, testNamespace), tc.Key+"-resource")

				provisionTimeout := tc.ProvisionTimeout
				if provisionTimeout == 0 {
					provisionTimeout = 10 * time.Minute
				}

				// Count readiness where the case supports it. A pod object appearing is not
				// the same as the service being up.
				countUp := podCount
				upDescription := "pod(s)"
				if tc.RequireReady {
					countUp = readyPodCount
					upDescription = "ready pod(s)"
				}

				By(fmt.Sprintf("waiting for the operator to provision %d %s", tc.ExpectedPods, upDescription))
				Eventually(func(g Gomega) {
					g.Expect(countUp(g, testNamespace, tc.PodSelector)).To(Equal(tc.ExpectedPods))
				}, provisionTimeout, 10*time.Second).Should(Succeed(),
					"the operator never provisioned its workloads. Check `kubectl -n %s get all`.", testNamespace)

				By("checking the field starts at its expected value")
				Expect(crField(Default, tc.CRRef, testNamespace, tc.FieldPath)).To(Equal(tc.FieldWhenUp))

				By("applying a schedule pinned to the downscale period")
				applySchedule(scheduleName, testNamespace, tc, false)

				By("waiting for lightsout to write the downscale value")
				Eventually(func(g Gomega) {
					g.Expect(crField(g, tc.CRRef, testNamespace, tc.FieldPath)).To(Equal(tc.FieldWhenOff))
				}, 3*time.Minute, 5*time.Second).Should(Succeed(),
					"lightsout did not patch the custom resource. Check the controller logs and the ClusterRole.")

				By("checking lightsout claimed the custom resource")
				Eventually(func(g Gomega) {
					g.Expect(crLabel(g, tc.CRRef, testNamespace, "lightsout.techsupport.mk/managed-by")).
						To(Equal(scheduleName))
				}, 1*time.Minute, 5*time.Second).Should(Succeed())

				By("waiting for the operator to remove its pods")
				Eventually(func(g Gomega) {
					g.Expect(podCount(g, testNamespace, tc.PodSelector)).To(Equal(0))
				}, 10*time.Minute, 10*time.Second).Should(Succeed(),
					"the operator did not honour the downscale. The recipe is wrong, not lightsout.")

				By("switching the schedule to the upscale period")
				applySchedule(scheduleName, testNamespace, tc, true)

				By("waiting for lightsout to restore the original value")
				Eventually(func(g Gomega) {
					g.Expect(crField(g, tc.CRRef, testNamespace, tc.FieldPath)).To(Equal(tc.FieldWhenUp))
				}, 3*time.Minute, 5*time.Second).Should(Succeed(),
					"lightsout did not restore the captured value")

				By(fmt.Sprintf("waiting for the operator to recreate its %s", upDescription))
				Eventually(func(g Gomega) {
					g.Expect(countUp(g, testNamespace, tc.PodSelector)).To(Equal(tc.ExpectedPods))
				}, provisionTimeout, 10*time.Second).Should(Succeed(),
					"the operator did not recreate its workloads after restore")

				By("checking lightsout released the custom resource after warmup")
				Eventually(func(g Gomega) {
					g.Expect(crLabel(g, tc.CRRef, testNamespace, "lightsout.techsupport.mk/managed-by")).
						To(BeEmpty())
				}, 15*time.Minute, 10*time.Second).Should(Succeed(),
					"lightsout kept the resource in warming-up. It releases the resource when the "+
						"pods report ready, or when customResourceWarmupTimeout elapses.")
			})
		})
	}
})

// applySchedule writes a LightsOutSchedule pinned to one period. The cron pair is
// chosen so the next transition is far away in both directions, which keeps the
// schedule in a fixed state for the duration of the assertion.
func applySchedule(name, testNamespace string, tc operatorCase, up bool) {
	upscale, downscale := "0 0 31 12 *", "0 0 1 1 *"
	if up {
		upscale, downscale = "0 0 1 1 *", "0 0 31 12 *"
	}

	includeOwned := ""
	if tc.IncludeOwnedWorkloads {
		includeOwned = "\n  includeOwnedWorkloads: true"
	}

	excludeLabels := ""
	if len(tc.ExcludeLabels) > 0 {
		keys := make([]string, 0, len(tc.ExcludeLabels))
		for k := range tc.ExcludeLabels {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var b strings.Builder
		b.WriteString("\n  excludeLabels:\n    matchLabels:")
		for _, k := range keys {
			fmt.Fprintf(&b, "\n      %s: %q", k, tc.ExcludeLabels[k])
		}
		excludeLabels = b.String()
	}

	manifest := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "%s"
  downscale: "%s"
  timezone: "UTC"
  customResourceWarmupTimeout: 5m%s%s
  namespaces:
    - %s%s
`, name, namespace, upscale, downscale, includeOwned, excludeLabels, testNamespace, tc.CustomResources)

	applyManifest(manifest, "schedule-"+tc.Key)
}

// applyOperatorRBAC grants lightsout's ServiceAccount access to one operator's
// custom resources, mirroring what the rbac.customResources Helm value renders.
func applyOperatorRBAC(name string, rules []rbacRule) {
	var b strings.Builder
	fmt.Fprintf(&b, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
`, name)

	for _, rule := range rules {
		verbs := rule.Verbs
		if len(verbs) == 0 {
			verbs = []string{"get", "list", "watch", "update", "patch"}
		}
		fmt.Fprintf(&b, "  - apiGroups: [%s]\n    resources: [%s]\n    verbs: [%s]\n",
			quoteList(rule.APIGroups), quoteList(rule.Resources), quoteList(verbs))
	}

	fmt.Fprintf(&b, `---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: %s
subjects:
  - kind: ServiceAccount
    name: lightsout-controller-manager
    namespace: %s
`, name, name, namespace)

	applyManifest(b.String(), "rbac-"+name)
}

// Tokens substituted into install, uninstall and readiness commands so a case can
// refer to the two namespaces it spans without hardcoding either.
const (
	// namespaceToken is the workload namespace, where the data plane lives and where
	// the schedule is pointed.
	namespaceToken = "{{NS}}"
	// operatorNamespaceToken is where the operator itself runs.
	operatorNamespaceToken = "{{OPNS}}"
)

func withNamespaces(argv []string, ns, opNS string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, namespaceToken, ns)
		out[i] = strings.ReplaceAll(arg, operatorNamespaceToken, opNS)
	}
	return out
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = strconv.Quote(item)
	}
	return strings.Join(quoted, ", ")
}

// applyManifest writes a manifest to a temp file and applies it, so multi-document
// YAML and long manifests do not have to go through the shell.
//
// The apply retries, because an operator that registers an admission webhook is not
// ready the moment its Deployment reports Available. The Service needs endpoints and
// kube-proxy needs to program them, and until both land the API server refuses the
// create with "connection refused". RabbitMQ lost that race by under a second.
func applyManifest(manifest, name string) {
	path := fmt.Sprintf("/tmp/lightsout-e2e-%s.yaml", name)
	Expect(os.WriteFile(path, []byte(manifest), 0o644)).To(Succeed())

	var out string
	Eventually(func(g Gomega) {
		var err error
		out, err = runQuiet("kubectl", "apply", "-f", path)
		g.Expect(err).NotTo(HaveOccurred())
	}, 2*time.Minute, 5*time.Second).Should(Succeed(),
		"failed to apply %s:\n%s\n%s", name, manifest, out)
}

// podMarker is emitted once per pod. Counting a fixed marker rather than pod names
// keeps the count correct even though utils.Run merges stderr into its output: any
// diagnostic kubectl writes cannot be mistaken for a pod.
const podMarker = "pod"

// runQuiet runs a command without writing it to the test log.
//
// utils.Run logs every invocation. That is useful for the steps of a case, but the
// polled helpers below run the same command many times while waiting, and each
// repetition adds a line that says nothing new. One case can produce over twenty
// identical lines, which hides the steps that matter.
func runQuiet(argv ...string) (string, error) {
	dir, _ := utils.GetProjectDir()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%q failed: %w", strings.Join(argv, " "), err)
	}
	return string(output), nil
}

// readyPodCount returns how many matching pods report the Ready condition. Used
// instead of podCount where a pod merely existing is too weak an assertion.
func readyPodCount(g Gomega, ns, selector string) int {
	output, err := runQuiet("kubectl", "get", "pods", "-n", ns, "-l", selector,
		"-o", `jsonpath={range .items[*]}{.status.conditions[?(@.type=='Ready')].status}{"\n"}{end}`)
	g.Expect(err).NotTo(HaveOccurred(), "listing pod readiness in %s matching %q", ns, selector)

	count := 0
	for _, line := range utils.GetNonEmptyLines(output) {
		if strings.TrimSpace(line) == "True" {
			count++
		}
	}
	return count
}

// podCount returns how many pods match the selector.
//
// A pod still terminating counts, because it is not gone yet. The callers poll until
// it is. Failures are asserted rather than folded into the return value, so a broken
// query reads as a broken query instead of "zero pods".
func podCount(g Gomega, ns, selector string) int {
	output, err := runQuiet("kubectl", "get", "pods", "-n", ns, "-l", selector,
		"-o", fmt.Sprintf(`jsonpath={range .items[*]}%s{"\n"}{end}`, podMarker))
	g.Expect(err).NotTo(HaveOccurred(), "listing pods in %s matching %q", ns, selector)

	count := 0
	for _, line := range utils.GetNonEmptyLines(output) {
		if strings.TrimSpace(line) == podMarker {
			count++
		}
	}
	return count
}

// crField reads a jsonpath out of a custom resource. A missing field yields "",
// which is exactly how an annotation that lightsout removed should read. A failed
// read is asserted rather than returned, so a broken query cannot masquerade as an
// absent field.
func crField(g Gomega, ref, ns, jsonPath string) string {
	output, err := runQuiet("kubectl", "get", ref, "-n", ns, "-o", "jsonpath="+jsonPath)
	g.Expect(err).NotTo(HaveOccurred(), "reading %s from %s in %s", jsonPath, ref, ns)
	return strings.TrimSpace(output)
}

func crLabel(g Gomega, ref, ns, label string) string {
	return crField(g, ref, ns, fmt.Sprintf("{.metadata.labels['%s']}", escapeAnnotationKey(label)))
}

// dumpDiagnostics prints what is actually in the cluster. Called when a spec fails so
// the log explains the failure instead of only naming the assertion that tripped.
//
// The operator's own namespace is included because the interesting failure is usually
// there rather than in the workload namespace: an operator that never reconciled leaves
// the workload namespace completely empty, with the reason in its own log.
func dumpDiagnostics(tc operatorCase, ns string) {
	cmds := [][]string{
		{"kubectl", "get", "all", "-n", ns},
		{"kubectl", "get", "events", "-n", ns, "--sort-by=.lastTimestamp"},
		// The data plane's own logs. Without these a crash-looping container looks
		// identical to an operator that never acted, and the log says which it is.
		// --previous as well, because a container in CrashLoopBackOff has already
		// restarted and its current attempt may still be starting up.
		{"kubectl", "logs", "-n", ns, "--tail=40", "--all-containers", "--prefix", "--ignore-errors", "-l", "app!=nonexistent"},
		{"kubectl", "logs", "-n", ns, "--tail=40", "--all-containers", "--prefix", "--previous", "--ignore-errors", "-l", "app!=nonexistent"},
	}
	if tc.CRRef != "" {
		cmds = append(cmds, []string{"kubectl", "get", tc.CRRef, "-n", ns, "-o", "yaml"})
	}
	if tc.OperatorNamespace != "" {
		cmds = append(cmds,
			[]string{"kubectl", "get", "all", "-n", tc.OperatorNamespace},
			[]string{"kubectl", "logs", "-n", tc.OperatorNamespace, "--tail=60", "--all-containers",
				"-l", "app.kubernetes.io/name!=nonexistent"},
		)
	}
	cmds = append(cmds,
		[]string{"kubectl", "logs", "-n", namespace, "deploy/lightsout-controller-manager", "--tail=80"})

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		output, err := utils.Run(cmd)
		if err != nil {
			output = fmt.Sprintf("(failed: %v)", err)
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "\n--- %s ---\n%s\n", strings.Join(args, " "), output)
	}
}
