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

package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

func jsonValue(t *testing.T, v any) apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %v: %v", v, err)
	}
	return apiextensionsv1.JSON{Raw: raw}
}

// newCNPGCluster builds a CloudNativePG Cluster, whose off switch is an annotation
// that does not exist until lightsout adds it.
func newCNPGCluster(name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster",
	})
	obj.SetName(name)
	obj.SetNamespace(namespace)
	_ = unstructured.SetNestedField(obj.Object, int64(3), "spec", "instances")
	return obj
}

// newElasticsearch builds an ECK Elasticsearch whose node counts live in an array,
// which is what the "*" wildcard exists for.
func newElasticsearch(name, namespace string, counts ...int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "elasticsearch.k8s.elastic.co", Version: "v1", Kind: "Elasticsearch",
	})
	obj.SetName(name)
	obj.SetNamespace(namespace)

	nodeSets := make([]any, 0, len(counts))
	for i, count := range counts {
		nodeSets = append(nodeSets, map[string]any{
			"name":  []string{"masters", "data", "ingest"}[i],
			"count": count,
		})
	}
	_ = unstructured.SetNestedSlice(obj.Object, nodeSets, "spec", "nodeSets")
	return obj
}

func cnpgConfig() *lightsoutv1alpha1.CustomResourceConfig {
	return &lightsoutv1alpha1.CustomResourceConfig{
		Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster",
	}
}

func getUnstructured(t *testing.T, c client.Client, src *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(src.GroupVersionKind())
	key := types.NamespacedName{Name: src.GetName(), Namespace: src.GetNamespace()}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("fetching %s: %v", src.GetName(), err)
	}
	return got
}

func TestTurnCustomResourceDown_CapturesAbsentAnnotation(t *testing.T) {
	cluster := newCNPGCluster("pg", "apps")
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(cluster).Build()

	cfg := cnpgConfig()
	cfg.SetFields = []lightsoutv1alpha1.FieldPatch{{
		Path:  "/metadata/annotations/cnpg.io~1hibernation",
		Value: jsonValue(t, "on"),
	}}

	obj := getUnstructured(t, c, cluster)
	skipped, err := TurnCustomResourceDown(context.Background(), c, obj, cfg, "sched")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped {
		t.Fatal("expected the resource to be turned down, not skipped")
	}

	got := getUnstructured(t, c, cluster)
	if got.GetAnnotations()["cnpg.io/hibernation"] != "on" {
		t.Errorf("hibernation annotation = %q, want \"on\"", got.GetAnnotations()["cnpg.io/hibernation"])
	}
	if got.GetLabels()[constants.StateLabel] != constants.StateDown {
		t.Errorf("state label = %q, want %q", got.GetLabels()[constants.StateLabel], constants.StateDown)
	}
	if got.GetLabels()[constants.ManagedByLabel] != "sched" {
		t.Errorf("managed-by label = %q, want \"sched\"", got.GetLabels()[constants.ManagedByLabel])
	}

	var captured map[string]capturedField
	if err := json.Unmarshal([]byte(got.GetAnnotations()[constants.OriginalFieldsAnnotation]), &captured); err != nil {
		t.Fatalf("decoding captured fields: %v", err)
	}
	field, ok := captured["/metadata/annotations/cnpg.io~1hibernation"]
	if !ok {
		t.Fatalf("expected the hibernation annotation to be captured, got %v", captured)
	}
	if field.Present {
		t.Error("expected the captured field to be recorded as absent before downscale")
	}
}

func TestRestoreCustomResourceFields_RemovesFieldThatDidNotExist(t *testing.T) {
	cluster := newCNPGCluster("pg", "apps")
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(cluster).Build()

	cfg := cnpgConfig()
	cfg.SetFields = []lightsoutv1alpha1.FieldPatch{{
		Path:  "/metadata/annotations/cnpg.io~1hibernation",
		Value: jsonValue(t, "on"),
	}}

	ctx := context.Background()
	obj := getUnstructured(t, c, cluster)
	if _, err := TurnCustomResourceDown(ctx, c, obj, cfg, "sched"); err != nil {
		t.Fatalf("turning down: %v", err)
	}

	obj = getUnstructured(t, c, cluster)
	skipped, err := RestoreCustomResourceFields(ctx, c, obj, "sched", time.Now())
	if err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if skipped {
		t.Fatal("expected the resource to be restored, not skipped")
	}

	got := getUnstructured(t, c, cluster)
	if _, present := got.GetAnnotations()["cnpg.io/hibernation"]; present {
		t.Error("hibernation annotation should be removed again: it did not exist before downscale")
	}
	if _, present := got.GetAnnotations()[constants.OriginalFieldsAnnotation]; present {
		t.Error("captured fields annotation should be cleared after restore")
	}
	if got.GetLabels()[constants.StateLabel] != constants.StateWarmingUp {
		t.Errorf("state label = %q, want %q", got.GetLabels()[constants.StateLabel], constants.StateWarmingUp)
	}
	if got.GetAnnotations()[constants.WarmingUpSinceAnnotation] == "" {
		t.Error("expected a warming-up-since timestamp after restore")
	}
}

func TestCustomResourceRoundTrip_WildcardRestoresEachNodeSet(t *testing.T) {
	es := newElasticsearch("es", "apps", 3, 5)
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(es).Build()

	cfg := &lightsoutv1alpha1.CustomResourceConfig{
		Group: "elasticsearch.k8s.elastic.co", Version: "v1", Kind: "Elasticsearch",
		SetFields: []lightsoutv1alpha1.FieldPatch{{
			Path:  "/spec/nodeSets/*/count",
			Value: jsonValue(t, 0),
		}},
	}

	ctx := context.Background()
	obj := getUnstructured(t, c, es)
	if _, err := TurnCustomResourceDown(ctx, c, obj, cfg, "sched"); err != nil {
		t.Fatalf("turning down: %v", err)
	}

	got := getUnstructured(t, c, es)
	nodeSets, _, _ := unstructured.NestedSlice(got.Object, "spec", "nodeSets")
	for i, entry := range nodeSets {
		if count := entry.(map[string]any)["count"]; count != int64(0) {
			t.Errorf("nodeSet %d count = %#v after downscale, want 0", i, count)
		}
	}

	obj = getUnstructured(t, c, es)
	if _, err := RestoreCustomResourceFields(ctx, c, obj, "sched", time.Now()); err != nil {
		t.Fatalf("restoring: %v", err)
	}

	got = getUnstructured(t, c, es)
	nodeSets, _, _ = unstructured.NestedSlice(got.Object, "spec", "nodeSets")
	want := []int64{3, 5}
	for i, entry := range nodeSets {
		if count := entry.(map[string]any)["count"]; count != want[i] {
			t.Errorf("nodeSet %d count = %#v after restore, want %d", i, count, want[i])
		}
	}
}

func TestTurnCustomResourceDown_SkipsForeignAndRepeatedRuns(t *testing.T) {
	ctx := context.Background()

	t.Run("managed by a different schedule", func(t *testing.T) {
		cluster := newCNPGCluster("pg", "apps")
		cluster.SetLabels(map[string]string{constants.ManagedByLabel: "other"})
		c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(cluster).Build()

		cfg := cnpgConfig()
		cfg.SetFields = []lightsoutv1alpha1.FieldPatch{{Path: "/spec/instances", Value: jsonValue(t, 0)}}

		skipped, err := TurnCustomResourceDown(ctx, c, getUnstructured(t, c, cluster), cfg, "sched")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !skipped {
			t.Error("expected a resource owned by another schedule to be skipped")
		}
	})

	t.Run("second run does not overwrite the captured original", func(t *testing.T) {
		cluster := newCNPGCluster("pg", "apps")
		c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(cluster).Build()

		cfg := cnpgConfig()
		cfg.SetFields = []lightsoutv1alpha1.FieldPatch{{Path: "/spec/instances", Value: jsonValue(t, 0)}}

		if _, err := TurnCustomResourceDown(ctx, c, getUnstructured(t, c, cluster), cfg, "sched"); err != nil {
			t.Fatalf("first run: %v", err)
		}
		skipped, err := TurnCustomResourceDown(ctx, c, getUnstructured(t, c, cluster), cfg, "sched")
		if err != nil {
			t.Fatalf("second run: %v", err)
		}
		if !skipped {
			t.Fatal("expected the second run to be skipped")
		}

		// The capture must still hold 3, not the 0 the first run wrote.
		obj := getUnstructured(t, c, cluster)
		if _, err := RestoreCustomResourceFields(ctx, c, obj, "sched", time.Now()); err != nil {
			t.Fatalf("restoring: %v", err)
		}
		got := getUnstructured(t, c, cluster)
		instances, _, _ := unstructured.NestedInt64(got.Object, "spec", "instances")
		if instances != 3 {
			t.Errorf("instances = %d after restore, want 3", instances)
		}
	})
}

func TestDeleteCustomResources(t *testing.T) {
	podSet := &unstructured.Unstructured{}
	podSet.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "core.strimzi.io", Version: "v1beta2", Kind: "StrimziPodSet",
	})
	podSet.SetName("kafka-brokers")
	podSet.SetNamespace("apps")

	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(podSet).Build()

	cfg := &lightsoutv1alpha1.CustomResourceConfig{
		Group: "core.strimzi.io", Version: "v1beta2", Kind: "StrimziPodSet", Delete: true,
	}

	deleted, err := DeleteCustomResources(context.Background(), c, cfg, []string{"apps"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(podSet.GroupVersionKind())
	err = c.Get(context.Background(), types.NamespacedName{Name: "kafka-brokers", Namespace: "apps"}, got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected the pod set to be gone, got err=%v", err)
	}

	// Running again with nothing left must be a no-op rather than an error.
	deleted, err = DeleteCustomResources(context.Background(), c, cfg, []string{"apps"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if deleted != 0 {
		t.Errorf("second run deleted = %d, want 0", deleted)
	}
}

func TestHandleCustomResourceWarmup(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding appsv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}

	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)

	// A warming-up cluster alongside the StatefulSet its operator manages.
	warmingUp := func(readyReplicas int32, since time.Time) (*unstructured.Unstructured, *appsv1.StatefulSet) {
		cluster := newCNPGCluster("pg", "apps")
		cluster.SetLabels(map[string]string{
			constants.ManagedByLabel: "sched",
			constants.StateLabel:     constants.StateWarmingUp,
		})
		cluster.SetAnnotations(map[string]string{
			constants.WarmingUpSinceAnnotation: since.UTC().Format(time.RFC3339),
		})
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "apps"},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: readyReplicas},
		}
		return cluster, sts
	}

	core := &lightsoutv1alpha1.LightsOutScheduleCore{
		CustomResources: []lightsoutv1alpha1.CustomResourceConfig{*cnpgConfig()},
	}

	t.Run("holds while workloads are not ready", func(t *testing.T) {
		cluster, sts := warmingUp(0, now.Add(-time.Minute))
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sts).Build()

		if !handleCustomResourceWarmup(context.Background(), c, nil, cluster, core, "sched", []string{"apps"}, now) {
			t.Error("expected warmup to still be in progress")
		}
		got := getUnstructured(t, c, cluster)
		if got.GetLabels()[constants.StateLabel] != constants.StateWarmingUp {
			t.Error("resource should stay in warming-up while workloads are not ready")
		}
	})

	t.Run("releases once workloads are ready", func(t *testing.T) {
		cluster, sts := warmingUp(3, now.Add(-time.Minute))
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sts).Build()

		if handleCustomResourceWarmup(context.Background(), c, nil, cluster, core, "sched", []string{"apps"}, now) {
			t.Error("expected warmup to finish once workloads are ready")
		}
		got := getUnstructured(t, c, cluster)
		if _, present := got.GetLabels()[constants.StateLabel]; present {
			t.Error("state label should be cleared once warmup completes")
		}
		if _, present := got.GetLabels()[constants.ManagedByLabel]; present {
			t.Error("managed-by label should be cleared once warmup completes")
		}
	})

	t.Run("releases when the timeout elapses", func(t *testing.T) {
		cluster, sts := warmingUp(0, now.Add(-30*time.Minute))
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sts).Build()

		if handleCustomResourceWarmup(context.Background(), c, nil, cluster, core, "sched", []string{"apps"}, now) {
			t.Error("expected warmup to be abandoned after the timeout")
		}
		got := getUnstructured(t, c, cluster)
		if _, present := got.GetLabels()[constants.StateLabel]; present {
			t.Error("state label should be cleared after the warmup timeout")
		}
	})
}

func TestDiscoverCustomResources_FiltersByNameAndLabels(t *testing.T) {
	a := newCNPGCluster("pg-a", "apps")
	a.SetLabels(map[string]string{"tier": "dev"})
	b := newCNPGCluster("pg-b", "apps")
	b.SetLabels(map[string]string{"tier": "prod"})
	other := newCNPGCluster("pg-a", "other")

	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(a, b, other).Build()
	ctx := context.Background()

	all, err := DiscoverCustomResources(ctx, c, cnpgConfig(), []string{"apps"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 resources in the apps namespace, got %d", len(all))
	}

	byName := cnpgConfig()
	byName.Name = "pg-b"
	named, err := DiscoverCustomResources(ctx, c, byName, []string{"apps"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(named) != 1 || named[0].GetName() != "pg-b" {
		t.Errorf("expected only pg-b, got %v", named)
	}

	byLabel := cnpgConfig()
	byLabel.MatchLabels = map[string]string{"tier": "dev"}
	labelled, err := DiscoverCustomResources(ctx, c, byLabel, []string{"apps"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(labelled) != 1 || labelled[0].GetName() != "pg-a" {
		t.Errorf("expected only pg-a, got %v", labelled)
	}
}

// TestReconcile_DefersWorkloadScaleUpUntilCustomResourcesReady covers the ordering
// guarantee: on upscale the custom resource is restored first, and application
// workloads stay scaled down until the workloads that resource manages report ready.
func TestReconcile_DefersWorkloadScaleUpUntilCustomResourcesReady(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, lightsoutv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"env": "crtest"}},
	}

	// The application workload, already scaled down by a previous downscale.
	app := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: "apps",
			Labels:    map[string]string{constants.ManagedByLabel: "cr-schedule"},
			Annotations: map[string]string{
				constants.ManagedByAnnotation:        "cr-schedule",
				constants.OriginalReplicasAnnotation: "2",
			},
		},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
	}

	// The database the operator manages: three desired replicas, none ready yet.
	db := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "pg",
			Namespace:       "apps",
			OwnerReferences: operatorOwned(),
		},
		Spec:   appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}

	// The custom resource, hibernated and owned by this schedule.
	cluster := newCNPGCluster("pg", "apps")
	cluster.SetLabels(map[string]string{
		constants.ManagedByLabel: "cr-schedule",
		constants.StateLabel:     constants.StateDown,
	})
	cluster.SetAnnotations(map[string]string{
		"cnpg.io/hibernation":              "on",
		constants.OriginalFieldsAnnotation: `{"/metadata/annotations/cnpg.io~1hibernation":{"present":false}}`,
	})

	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "cr-schedule"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				// Distinct cron expressions: CalculatePeriod caches by expression,
				// so sharing them with another test leaks state across runs.
				Upscale:         "0 5 * * *",
				Downscale:       "0 21 * * *",
				CustomResources: []lightsoutv1alpha1.CustomResourceConfig{*cnpgConfig()},
			},
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "crtest"},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, app, db, cluster, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutScheduleReconciler{
		Client: c,
		Scheme: scheme,
		// 09:00 sits inside the Up window for this schedule.
		TimeFunc: func() time.Time { return time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: "cr-schedule"}}
	ctx := context.Background()

	// First reconcile only installs the finalizer.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	// Second reconcile restores the custom resource and starts the wait.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("upscale reconcile: %v", err)
	}

	gotCluster := getUnstructured(t, c, cluster)
	if _, present := gotCluster.GetAnnotations()["cnpg.io/hibernation"]; present {
		t.Error("expected the hibernation annotation to be removed on upscale")
	}
	if gotCluster.GetLabels()[constants.StateLabel] != constants.StateWarmingUp {
		t.Errorf("state label = %q, want %q",
			gotCluster.GetLabels()[constants.StateLabel], constants.StateWarmingUp)
	}

	gotApp := &appsv1.Deployment{}
	if err := c.Get(ctx, client.ObjectKey{Name: "app", Namespace: "apps"}, gotApp); err != nil {
		t.Fatalf("fetching app: %v", err)
	}
	if *gotApp.Spec.Replicas != 0 {
		t.Errorf("app replicas = %d, want 0: scale-up must wait for the database",
			*gotApp.Spec.Replicas)
	}

	// The database becomes ready, so the next reconcile releases the wait and scales up.
	gotDB := &appsv1.StatefulSet{}
	if err := c.Get(ctx, client.ObjectKey{Name: "pg", Namespace: "apps"}, gotDB); err != nil {
		t.Fatalf("fetching db: %v", err)
	}
	gotDB.Status.ReadyReplicas = 3
	if err := c.Status().Update(ctx, gotDB); err != nil {
		t.Fatalf("marking db ready: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second upscale reconcile: %v", err)
	}

	if err := c.Get(ctx, client.ObjectKey{Name: "app", Namespace: "apps"}, gotApp); err != nil {
		t.Fatalf("fetching app: %v", err)
	}
	if *gotApp.Spec.Replicas != 2 {
		t.Errorf("app replicas = %d, want 2 once the database is ready", *gotApp.Spec.Replicas)
	}

	gotCluster = getUnstructured(t, c, cluster)
	if _, present := gotCluster.GetLabels()[constants.ManagedByLabel]; present {
		t.Error("expected lightsout labels to be cleared once warmup completed")
	}
}

// TestDocumentedRecipesParse guards the per-operator stanzas in
// docs/custom-resources.md: every one must decode into the API type and every field
// path must be a pointer the engine can resolve. Without this the documented recipes
// can rot silently.
func TestDocumentedRecipesParse(t *testing.T) {
	const recipes = `
customResources:
  - group: postgresql.cnpg.io
    version: v1
    kind: Cluster
    setFields:
      - path: /metadata/annotations/cnpg.io~1hibernation
        value: "on"
  - group: elasticsearch.k8s.elastic.co
    version: v1
    kind: Elasticsearch
    setFields:
      - path: /metadata/annotations/eck.k8s.elastic.co~1pause-orchestration
        value: "true"
  - group: monitoring.coreos.com
    version: v1
    kind: Prometheus
    setFields:
      - path: /spec/replicas
        value: 0
  - group: monitoring.coreos.com
    version: v1
    kind: Alertmanager
    setFields:
      - path: /spec/replicas
        value: 0
  - group: k8s.keycloak.org
    version: v2beta1
    kind: Keycloak
    setFields:
      - path: /spec/instances
        value: 0
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
  - group: k8s.mariadb.com
    version: v1alpha1
    kind: MariaDB
    setFields:
      - path: /spec/suspend
        value: true
  - group: redis.redis.opstreelabs.in
    version: v1beta2
    kind: RedisCluster
    setFields:
      - path: /metadata/annotations/rediscluster.opstreelabs.in~1skip-reconcile
        value: "true"
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
`

	var core lightsoutv1alpha1.LightsOutScheduleCore
	if err := yaml.Unmarshal([]byte(recipes), &core); err != nil {
		t.Fatalf("decoding documented recipes: %v", err)
	}

	if len(core.CustomResources) != 10 {
		t.Fatalf("expected 10 documented entries, got %d", len(core.CustomResources))
	}

	for _, cfg := range core.CustomResources {
		if cfg.Version == "" || cfg.Kind == "" {
			t.Errorf("%s/%s: version and kind are required", cfg.Group, cfg.Kind)
		}
		if cfg.Delete {
			if len(cfg.SetFields) != 0 {
				t.Errorf("%s: a delete entry should not also set fields", cfg.Kind)
			}
			continue
		}
		if len(cfg.SetFields) == 0 {
			t.Errorf("%s: entry sets no fields and does not delete", cfg.Kind)
		}
		for _, field := range cfg.SetFields {
			if _, err := parsePointer(field.Path); err != nil {
				t.Errorf("%s: path %q: %v", cfg.Kind, field.Path, err)
			}
			var decoded any
			if err := json.Unmarshal(field.Value.Raw, &decoded); err != nil {
				t.Errorf("%s: value for %q is not valid JSON: %v", cfg.Kind, field.Path, err)
			}
		}
	}

	// The Strimzi pause entry must be declared before the entry that deletes what the
	// operator manages, or the operator recreates the pod set immediately.
	pause, del := -1, -1
	for i, cfg := range core.CustomResources {
		switch cfg.Kind {
		case "Kafka":
			pause = i
		case "StrimziPodSet":
			del = i
		}
	}
	if pause < 0 || del < 0 || pause > del {
		t.Errorf("Strimzi pause entry (index %d) must come before the delete entry (index %d)", pause, del)
	}
}

// TestClearCustomResourceStateSurvivesAStaleObject reproduces what an active
// operator does to the release step. ClickHouse rewrites its own CR status on a
// timer, so the copy warmup holds is often a resourceVersion behind by the time it
// releases. An Update fails that with "the object has been modified". A merge patch
// carries no resourceVersion, so the release goes through anyway.
func TestClearCustomResourceStateSurvivesAStaleObject(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}

	cluster := newCNPGCluster("pg", "apps")
	cluster.SetLabels(map[string]string{
		constants.ManagedByLabel: "sched",
		constants.StateLabel:     constants.StateWarmingUp,
		"unrelated":              "keep-me",
	})
	cluster.SetAnnotations(map[string]string{
		constants.OriginalFieldsAnnotation:   `{"/spec/instances":{"present":true,"value":3}}`,
		constants.WarmingUpSinceAnnotation:   "2026-09-20T08:00:00Z",
		"operator.example/status-generation": "7",
	})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	ctx := context.Background()
	key := client.ObjectKeyFromObject(cluster)

	// The operator writes to the resource, which moves it past the copy in hand.
	live := newCNPGCluster("pg", "apps")
	if err := c.Get(ctx, key, live); err != nil {
		t.Fatalf("reading the resource back: %v", err)
	}
	annotations := live.GetAnnotations()
	annotations["operator.example/status-generation"] = "8"
	live.SetAnnotations(annotations)
	if err := c.Update(ctx, live); err != nil {
		t.Fatalf("simulating the operator's write: %v", err)
	}

	// `cluster` is now stale. Releasing must still work.
	if err := clearCustomResourceState(ctx, c, cluster); err != nil {
		t.Fatalf("clearing state with a stale object: %v", err)
	}

	var got unstructured.Unstructured
	got.SetGroupVersionKind(cluster.GroupVersionKind())
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatalf("reading the released resource: %v", err)
	}

	for _, k := range []string{constants.ManagedByLabel, constants.StateLabel} {
		if _, exists := got.GetLabels()[k]; exists {
			t.Errorf("label %q should be gone", k)
		}
	}
	for _, k := range []string{constants.OriginalFieldsAnnotation, constants.WarmingUpSinceAnnotation} {
		if _, exists := got.GetAnnotations()[k]; exists {
			t.Errorf("annotation %q should be gone", k)
		}
	}
	// Only lightsout's own keys go. Everything else belongs to someone else.
	if got.GetLabels()["unrelated"] != "keep-me" {
		t.Error("an unrelated label was removed")
	}
	if got.GetAnnotations()["operator.example/status-generation"] != "8" {
		t.Error("the operator's own annotation was lost or reverted")
	}
}
