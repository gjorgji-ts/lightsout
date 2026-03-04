package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

const testNS = "team-a"

func TestNamespaceScheduleReconcile_ScalesDown(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	ns := testNS
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "team-schedule", Namespace: ns},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutNamespaceScheduleReconciler{Client: c, Scheme: scheme}
	// 20:00 UTC → downscale period
	r.TimeFunc = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-01-01T20:00:00Z")
		return t
	}

	// First reconcile: adds finalizer, returns early
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "team-schedule", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	// Second reconcile: does actual scaling
	_, err = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "team-schedule", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error on second reconcile: %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: ns}, &d); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Errorf("expected deployment scaled to 0, got %v", d.Spec.Replicas)
	}
}

func TestNamespaceScheduleReconcile_OnlyManagesOwnNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	deployInOwner := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-a", Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
	}
	deployInOther := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-b", Namespace: "team-b"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
	}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "schedule", Namespace: testNS},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployInOwner, deployInOther, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutNamespaceScheduleReconciler{Client: c, Scheme: scheme}
	r.TimeFunc = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-01-01T20:00:00Z")
		return t
	}

	// Two reconciles: first adds finalizer, second scales
	for i := 0; i < 2; i++ {
		_, err := r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "schedule", Namespace: testNS},
		})
		if err != nil {
			t.Fatalf("reconcile %d: unexpected error: %v", i+1, err)
		}
	}

	// team-a deployment should be scaled to 0
	var dA appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-a", Namespace: testNS}, &dA); err != nil {
		t.Fatalf("failed to get team-a deployment: %v", err)
	}
	if dA.Spec.Replicas == nil || *dA.Spec.Replicas != 0 {
		t.Errorf("expected team-a deployment scaled to 0, got %v", dA.Spec.Replicas)
	}

	// team-b deployment should NOT be touched
	var dB appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-b", Namespace: "team-b"}, &dB); err != nil {
		t.Fatalf("failed to get team-b deployment: %v", err)
	}
	if dB.Spec.Replicas == nil || *dB.Spec.Replicas != 2 {
		t.Errorf("expected team-b deployment untouched (replicas=2), got %v", dB.Spec.Replicas)
	}
}

func TestNamespaceScheduleReconcile_Suspended(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	ns := testNS
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "team-schedule", Namespace: ns},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
				Suspend:   true,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutNamespaceScheduleReconciler{Client: c, Scheme: scheme}
	r.TimeFunc = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-01-01T20:00:00Z")
		return t
	}

	// Two reconciles: first adds finalizer, second checks suspension
	for i := 0; i < 2; i++ {
		_, err := r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "team-schedule", Namespace: ns},
		})
		if err != nil {
			t.Fatalf("reconcile %d: unexpected error: %v", i+1, err)
		}
	}

	// Deployment should NOT have been scaled (schedule is suspended)
	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: ns}, &d); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas == 0 {
		t.Errorf("expected deployment to remain at 3 replicas (suspended), got %v", d.Spec.Replicas)
	}
}

func TestNamespaceScheduleReconcile_ClaimsGloballyOwnedWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	ns := testNS
	// Deployment previously scaled to 0 by a global schedule
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: ns,
			Annotations: map[string]string{
				"lightsout.techsupport.mk/managed-by":        "global-schedule",
				"lightsout.techsupport.mk/original-replicas": "3",
			},
			Labels: map[string]string{
				"lightsout.techsupport.mk/managed-by": "global-schedule",
			},
		},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
	}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "team-schedule", Namespace: ns},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 7 * * *",
				Downscale: "0 19 * * *",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutNamespaceScheduleReconciler{Client: c, Scheme: scheme}
	// 10:00 UTC → upscale period (after 07:00 upscale, before 19:00 downscale)
	r.TimeFunc = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-01-01T10:00:00Z")
		return t
	}

	// Two reconciles: first adds finalizer, second does actual scaling
	for i := 0; i < 2; i++ {
		_, err := r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "team-schedule", Namespace: ns},
		})
		if err != nil {
			t.Fatalf("reconcile %d: unexpected error: %v", i+1, err)
		}
	}

	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: ns}, &d); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	// Namespace schedule must have claimed the workload and restored it
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 3 {
		t.Errorf("expected deployment restored to 3 replicas, got %v", d.Spec.Replicas)
	}
	if d.Labels["lightsout.techsupport.mk/managed-by"] != "" {
		t.Errorf("expected managed-by label removed after upscale, got %q", d.Labels["lightsout.techsupport.mk/managed-by"])
	}
}

func TestNamespaceScheduleReconcile_Deletion(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	ns := testNS
	now := metav1.Now()
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "team-schedule",
			Namespace:         ns,
			DeletionTimestamp: &now,
			Finalizers:        []string{"lightsout.techsupport.mk/cleanup"},
		},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutNamespaceScheduleReconciler{Client: c, Scheme: scheme}
	r.TimeFunc = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-01-01T20:00:00Z")
		return t
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "team-schedule", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Finalizer should have been removed: either the object is gone (fake client
	// garbage-collects it once the last finalizer is removed) or it exists without
	// the finalizer.
	var updated lightsoutv1alpha1.LightsOutNamespaceSchedule
	err = c.Get(context.Background(), types.NamespacedName{Name: "team-schedule", Namespace: ns}, &updated)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("unexpected error getting schedule: %v", err)
		}
		// Object is gone — finalizer was successfully removed
		return
	}
	for _, f := range updated.Finalizers {
		if f == "lightsout.techsupport.mk/cleanup" {
			t.Error("expected finalizer to be removed after deletion reconcile")
		}
	}
}
