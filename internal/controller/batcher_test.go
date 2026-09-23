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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

func TestWorkloadFromDeployment(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromDeployment(deploy)
	if workload.Type != WorkloadTypeDeployment {
		t.Errorf("expected type Deployment, got %s", workload.Type)
	}
	if workload.Name != "my-deploy" {
		t.Errorf("expected name my-deploy, got %s", workload.Name)
	}
	if workload.Namespace != "my-ns" {
		t.Errorf("expected namespace my-ns, got %s", workload.Namespace)
	}
	if workload.Deployment != deploy {
		t.Error("expected Deployment pointer to be set")
	}
}

func TestWorkloadFromStatefulSet(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-sts",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromStatefulSet(sts)
	if workload.Type != WorkloadTypeStatefulSet {
		t.Errorf("expected type StatefulSet, got %s", workload.Type)
	}
	if workload.StatefulSet != sts {
		t.Error("expected StatefulSet pointer to be set")
	}
}

func TestWorkloadFromCronJob(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cj",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromCronJob(cj)
	if workload.Type != WorkloadTypeCronJob {
		t.Errorf("expected type CronJob, got %s", workload.Type)
	}
	if workload.CronJob != cj {
		t.Error("expected CronJob pointer to be set")
	}
}

func TestHasControllerOwner(t *testing.T) {
	tests := []struct {
		name string
		refs []metav1.OwnerReference
		want bool
	}{
		{"no owner references", nil, false},
		{"empty owner references", []metav1.OwnerReference{}, false},
		{
			"non-controller owner reference",
			[]metav1.OwnerReference{{Kind: "Cluster", Name: "pg"}},
			false,
		},
		{
			"explicitly non-controlling owner reference",
			[]metav1.OwnerReference{{Kind: "Cluster", Name: "pg", Controller: ptr(false)}},
			false,
		},
		{
			"controller owner reference",
			[]metav1.OwnerReference{{Kind: "Elasticsearch", Name: "es", Controller: ptr(true)}},
			true,
		},
		{
			"controller among several owner references",
			[]metav1.OwnerReference{
				{Kind: "Cluster", Name: "pg"},
				{Kind: "Elasticsearch", Name: "es", Controller: ptr(true)},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasControllerOwner(tt.refs); got != tt.want {
				t.Errorf("hasControllerOwner() = %v, want %v", got, tt.want)
			}
		})
	}
}

// operatorOwned marks an object as controlled by another controller, the way an
// operator-created StatefulSet is owned by its custom resource.
func operatorOwned() []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: "elasticsearch.k8s.elastic.co/v1",
		Kind:       "Elasticsearch",
		Name:       "es-cluster",
		Controller: ptr(true),
	}}
}

func TestCollectWorkloads_SkipsOperatorOwnedWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "owned-deploy", Namespace: "ns1", OwnerReferences: operatorOwned()},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
			},
			&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "owned-sts", Namespace: "ns1", OwnerReferences: operatorOwned()},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
			},
			&batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "owned-cj", Namespace: "ns1", OwnerReferences: operatorOwned()},
				Spec:       batchv1.CronJobSpec{Suspend: ptr(false)},
			},
		).
		Build()

	t.Run("skipped by default", func(t *testing.T) {
		core := &lightsoutv1alpha1.LightsOutScheduleCore{}
		workloads, err := collectWorkloads(context.Background(), fakeClient, []string{"ns1"}, core, "sched", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(workloads) != 1 {
			t.Fatalf("expected only the unowned deployment, got %d workloads", len(workloads))
		}
		if workloads[0].Name != "app" {
			t.Errorf("expected workload %q, got %q", "app", workloads[0].Name)
		}
	})

	t.Run("included when includeOwnedWorkloads is set", func(t *testing.T) {
		core := &lightsoutv1alpha1.LightsOutScheduleCore{IncludeOwnedWorkloads: true}
		workloads, err := collectWorkloads(context.Background(), fakeClient, []string{"ns1"}, core, "sched", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(workloads) != 4 {
			t.Errorf("expected all 4 workloads, got %d", len(workloads))
		}
	})
}

func TestScaleWorkloads_StatsExcludeSkippedWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			// Scaled down by this run.
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "ns1"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
			},
			// Parked at zero by a user: skipped, and not ours to report as scaled.
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "user-parked", Namespace: "ns1"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			// Owned by another schedule: skipped, and not ours to report as scaled.
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "other-schedule",
					Namespace:   "ns1",
					Annotations: map[string]string{constants.ManagedByAnnotation: "somebody-else"},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
			},
			// Already down under this schedule: skipped, but still down, so counted.
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "already-down",
					Namespace: "ns1",
					Annotations: map[string]string{
						constants.ManagedByAnnotation:        "test-schedule",
						constants.OriginalReplicasAnnotation: "4",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
		).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule"},
	}

	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.stats.DeploymentsManaged != 4 {
		t.Errorf("expected 4 deployments managed, got %d", result.stats.DeploymentsManaged)
	}
	// "active" was scaled now, "already-down" was already down. The user-parked and
	// other-schedule deployments must not be reported as scaled by us.
	if result.stats.DeploymentsScaled != 2 {
		t.Errorf("expected 2 deployments scaled, got %d", result.stats.DeploymentsScaled)
	}
	if result.totalSkipped != 3 {
		t.Errorf("expected 3 skipped, got %d", result.totalSkipped)
	}
}

func TestScaleWorkloads_StatsClearOnUpscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restored",
				Namespace: "ns1",
				Annotations: map[string]string{
					constants.ManagedByAnnotation:        "test-schedule",
					constants.OriginalReplicasAnnotation: "5",
				},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
		}).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule"},
	}

	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.stats.DeploymentsManaged != 1 {
		t.Errorf("expected 1 deployment managed, got %d", result.stats.DeploymentsManaged)
	}
	if result.stats.DeploymentsScaled != 0 {
		t.Errorf("expected 0 deployments scaled after upscale, got %d", result.stats.DeploymentsScaled)
	}
}
