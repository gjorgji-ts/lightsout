package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

//nolint:unparam
func newFluxKustomization(name, namespace, targetNamespace string, labels map[string]string, suspended bool) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	})
	obj.SetName(name)
	obj.SetNamespace(namespace)
	if labels != nil {
		obj.SetLabels(labels)
	}
	if targetNamespace != "" {
		_ = unstructured.SetNestedField(obj.Object, targetNamespace, "spec", "targetNamespace")
	}
	_ = unstructured.SetNestedField(obj.Object, suspended, "spec", "suspend")
	return obj
}

func newFluxHelmRelease(name, namespace, targetNamespace string, labels map[string]string, suspended bool) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	obj.SetName(name)
	obj.SetNamespace(namespace)
	if labels != nil {
		obj.SetLabels(labels)
	}
	if targetNamespace != "" {
		_ = unstructured.SetNestedField(obj.Object, targetNamespace, "spec", "targetNamespace")
	}
	_ = unstructured.SetNestedField(obj.Object, suspended, "spec", "suspend")
	return obj
}

func TestDiscoverFluxResources_ByTargetNamespace(t *testing.T) {
	scheme := runtime.NewScheme()

	ks1 := newFluxKustomization("ks-dev", "flux-system", "dev", nil, false)
	ks2 := newFluxKustomization("ks-staging", "flux-system", "staging", nil, false)
	hr1 := newFluxHelmRelease("hr-dev", "flux-system", "dev", nil, false)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ks1, ks2, hr1).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 2 {
		t.Errorf("got %d resources, want 2 (ks-dev + hr-dev)", len(resources))
	}
}

func TestDiscoverFluxResources_CoLocated(t *testing.T) {
	scheme := runtime.NewScheme()

	// HelmRelease lives in the app namespace itself (no spec.targetNamespace)
	hr := newFluxHelmRelease("hr-app", "team-a", "", nil, false)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"team-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("got %d resources, want 1", len(resources))
	}
}

func TestDiscoverFluxResources_Deduplication(t *testing.T) {
	scheme := runtime.NewScheme()

	// HelmRelease lives in flux-system AND has targetNamespace=flux-system (edge case)
	// It should only appear once.
	hr := newFluxHelmRelease("hr-self", "flux-system", "flux-system", nil, false)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"flux-system"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("got %d resources, want 1 (no duplicates)", len(resources))
	}
}

func TestDiscoverFluxResources_NoMatch(t *testing.T) {
	scheme := runtime.NewScheme()

	ks := newFluxKustomization("ks-prod", "flux-system", "prod", nil, false)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ks).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("got %d resources, want 0", len(resources))
	}
}

func TestDiscoverFluxResources_MultiTenantNamespace(t *testing.T) {
	scheme := runtime.NewScheme()

	// HelmRelease lives in a team namespace (not flux-system), targets an app namespace.
	// This is the multi-tenant pattern: prior to the cluster-wide fix this was not discovered.
	hr := newFluxHelmRelease("hr-app", "team-a", "my-app", nil, false)
	ks := newFluxKustomization("ks-other", "flux-system", "other", nil, false) // should not match

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr, ks).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"my-app"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("got %d resources, want 1 (hr-app in team-a)", len(resources))
	}
	if len(resources) == 1 && resources[0].GetName() != "hr-app" {
		t.Errorf("got resource %q, want hr-app", resources[0].GetName())
	}
}

func TestDiscoverFluxResources_DefaultNamespace(t *testing.T) {
	scheme := runtime.NewScheme()

	ks := newFluxKustomization("ks-dev", "flux-system", "dev", nil, false)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ks).Build()

	// Empty namespace should default to "flux-system"
	cfg := &lightsoutv1alpha1.FluxCDConfig{}
	resources, err := DiscoverFluxResources(context.Background(), fakeClient, cfg, []string{"dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("got %d resources, want 1", len(resources))
	}
}

// Note: the meta.IsNoMatchError path (FluxCD CRDs not installed on cluster) cannot be
// triggered via the fake client, the fake client returns empty lists for unknown GVKs
// rather than a NoMatchError. That code path mirrors argocd.go exactly and is tested
// via integration/e2e tests when FluxCD is absent.

func TestSuspendFluxResource(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := []struct {
		name          string
		obj           *unstructured.Unstructured
		scheduleName  string
		wantSkipped   bool
		wantSuspended bool
		wantState     string
		wantManagedBy string
	}{
		{
			name:          "suspend unsuspended resource",
			obj:           newFluxKustomization("ks-dev", "flux-system", "dev", nil, false),
			scheduleName:  "dev-schedule",
			wantSuspended: true,
			wantState:     constants.StateDown,
			wantManagedBy: "dev-schedule",
		},
		{
			name:         "skip user-suspended resource (no managed-by label)",
			obj:          newFluxKustomization("ks-dev", "flux-system", "dev", nil, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "skip resource managed by different schedule",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "other-schedule",
				constants.StateLabel:     constants.StateDown,
			}, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "idempotent: already suspended by this schedule",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateDown,
			}, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.obj).Build()

			skipped, err := SuspendFluxResource(context.Background(), fakeClient, tt.obj, tt.scheduleName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}

			if !tt.wantSkipped {
				var updated unstructured.Unstructured
				updated.SetGroupVersionKind(tt.obj.GroupVersionKind())
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.obj), &updated); err != nil {
					t.Fatalf("failed to get resource: %v", err)
				}
				suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
				if suspended != tt.wantSuspended {
					t.Errorf("spec.suspend = %v, want %v", suspended, tt.wantSuspended)
				}
				labels := updated.GetLabels()
				if labels[constants.StateLabel] != tt.wantState {
					t.Errorf("state label = %q, want %q", labels[constants.StateLabel], tt.wantState)
				}
				if labels[constants.ManagedByLabel] != tt.wantManagedBy {
					t.Errorf("managed-by = %q, want %q", labels[constants.ManagedByLabel], tt.wantManagedBy)
				}
			}
		})
	}
}

func TestSuspendFluxResource_ClearsWarmingUpAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()

	// Edge case: downscale fires while resource is in warming-up state (suspend=false, just scaled up)
	obj := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, false)
	obj.SetAnnotations(map[string]string{
		constants.WarmingUpSinceAnnotation: "2026-03-21T08:00:00Z",
	})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()

	skipped, err := SuspendFluxResource(context.Background(), fakeClient, obj, "dev-schedule")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped {
		t.Errorf("expected not skipped")
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(obj.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(obj), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}
	if _, exists := updated.GetAnnotations()[constants.WarmingUpSinceAnnotation]; exists {
		t.Errorf("warming-up-since annotation should be cleared when suspending")
	}
}

func TestSuspendFluxResource_WarmingUpSuspendedTransitionsToDown(t *testing.T) {
	scheme := runtime.NewScheme()

	// Edge case: resource is warming-up AND already suspended (e.g. a previous suspend
	// was interrupted mid-reconcile). Should transition to state=down and clear the annotation.
	obj := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, true) // suspended=true
	obj.SetAnnotations(map[string]string{
		constants.WarmingUpSinceAnnotation: "2026-03-21T08:00:00Z",
	})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()

	skipped, err := SuspendFluxResource(context.Background(), fakeClient, obj, "dev-schedule")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped {
		t.Errorf("expected not skipped: warming-up resource should be transitioned to down")
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(obj.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(obj), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}
	if updated.GetLabels()[constants.StateLabel] != constants.StateDown {
		t.Errorf("state = %q, want down", updated.GetLabels()[constants.StateLabel])
	}
	if _, exists := updated.GetAnnotations()[constants.WarmingUpSinceAnnotation]; exists {
		t.Errorf("warming-up-since annotation should be cleared")
	}
}

func TestTransitionFluxResourceToWarmingUp(t *testing.T) {
	scheme := runtime.NewScheme()
	now := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		obj          *unstructured.Unstructured
		scheduleName string
		wantSkipped  bool
	}{
		{
			name: "transition down resource to warming-up",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateDown,
			}, true),
			scheduleName: "dev-schedule",
		},
		{
			name: "idempotent: already warming-up",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateWarmingUp,
			}, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "skip resource managed by different schedule",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "other-schedule",
			}, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.obj).Build()

			skipped, err := TransitionFluxResourceToWarmingUp(context.Background(), fakeClient, tt.obj, tt.scheduleName, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}

			if !tt.wantSkipped {
				var updated unstructured.Unstructured
				updated.SetGroupVersionKind(tt.obj.GroupVersionKind())
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.obj), &updated); err != nil {
					t.Fatalf("failed to get resource: %v", err)
				}
				labels := updated.GetLabels()
				if labels[constants.StateLabel] != constants.StateWarmingUp {
					t.Errorf("state = %q, want warming-up", labels[constants.StateLabel])
				}
				wantTS := now.UTC().Format(time.RFC3339)
				if updated.GetAnnotations()[constants.WarmingUpSinceAnnotation] != wantTS {
					t.Errorf("warming-up-since = %q, want %q", updated.GetAnnotations()[constants.WarmingUpSinceAnnotation], wantTS)
				}
				// spec.suspend must stay true during warmup
				suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
				if !suspended {
					t.Errorf("spec.suspend should remain true during warmup")
				}
			}
		})
	}
}

func TestCompleteFluxWarmup(t *testing.T) {
	scheme := runtime.NewScheme()

	obj := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, true)
	obj.SetAnnotations(map[string]string{
		constants.WarmingUpSinceAnnotation: "2026-03-21T08:00:00Z",
	})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()

	if err := CompleteFluxWarmup(context.Background(), fakeClient, obj); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(obj.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(obj), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}

	if _, exists := updated.GetLabels()[constants.StateLabel]; exists {
		t.Errorf("state label should be removed")
	}
	if _, exists := updated.GetLabels()[constants.ManagedByLabel]; exists {
		t.Errorf("managed-by label should be removed")
	}
	if _, exists := updated.GetAnnotations()[constants.WarmingUpSinceAnnotation]; exists {
		t.Errorf("warming-up-since annotation should be removed")
	}
	suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
	if suspended {
		t.Errorf("spec.suspend should be false after warmup completes")
	}
}

func TestResumeFluxResource(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := []struct {
		name         string
		obj          *unstructured.Unstructured
		scheduleName string
		wantSkipped  bool
	}{
		{
			name: "resume resource managed by this schedule",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateDown,
			}, true),
			scheduleName: "dev-schedule",
		},
		{
			name:         "skip resource without managed-by label",
			obj:          newFluxKustomization("ks-dev", "flux-system", "dev", nil, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "skip resource managed by different schedule",
			obj: newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
				constants.ManagedByLabel: "other-schedule",
			}, true),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.obj).Build()

			skipped, err := ResumeFluxResource(context.Background(), fakeClient, tt.obj, tt.scheduleName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}

			if !tt.wantSkipped {
				var updated unstructured.Unstructured
				updated.SetGroupVersionKind(tt.obj.GroupVersionKind())
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.obj), &updated); err != nil {
					t.Fatalf("failed to get resource: %v", err)
				}
				suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
				if suspended {
					t.Errorf("spec.suspend should be false after resume")
				}
				if _, exists := updated.GetLabels()[constants.StateLabel]; exists {
					t.Errorf("state label should be removed")
				}
			}
		})
	}
}

func TestHandleFluxCDWarmup_TransitionsDownToWarmingUp(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	now := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)

	ks := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateDown,
	}, true)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ks).Build()
	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}

	stillWarmingUp := handleFluxCDWarmup(context.Background(), fakeClient, nil, nil, cfg, "dev-schedule", []string{"dev"}, now)
	if !stillWarmingUp {
		t.Errorf("expected stillWarmingUp = true when resource just transitioned to warming-up")
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(ks.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ks), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}
	if updated.GetLabels()[constants.StateLabel] != constants.StateWarmingUp {
		t.Errorf("state = %q, want warming-up", updated.GetLabels()[constants.StateLabel])
	}
}

func TestHandleFluxCDWarmup_CompletesWhenPodsReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	now := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)
	warmingUpSince := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)

	ks := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, true)
	ks.SetAnnotations(map[string]string{constants.WarmingUpSinceAnnotation: warmingUpSince})

	// All deployments in "dev" namespace are ready
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "dev"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ks, deploy).
		WithStatusSubresource(deploy).
		Build()

	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}
	stillWarmingUp := handleFluxCDWarmup(context.Background(), fakeClient, nil, nil, cfg, "dev-schedule", []string{"dev"}, now)
	if stillWarmingUp {
		t.Errorf("expected stillWarmingUp = false when pods are ready")
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(ks.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ks), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}
	suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
	if suspended {
		t.Errorf("spec.suspend should be false after warmup completes")
	}
}

func TestHandleFluxCDWarmup_CompletesOnTimeout(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	now := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)
	// warmingUpSince is 15 minutes ago exceeds default 10m timeout
	warmingUpSince := now.Add(-15 * time.Minute).UTC().Format(time.RFC3339)

	ks := newFluxKustomization("ks-dev", "flux-system", "dev", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, true)
	ks.SetAnnotations(map[string]string{constants.WarmingUpSinceAnnotation: warmingUpSince})

	// Deployment not yet ready but timeout should override
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "dev"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ks, deploy).
		WithStatusSubresource(deploy).
		Build()

	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}
	stillWarmingUp := handleFluxCDWarmup(context.Background(), fakeClient, nil, nil, cfg, "dev-schedule", []string{"dev"}, now)
	if stillWarmingUp {
		t.Errorf("expected stillWarmingUp = false after timeout elapses")
	}

	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(ks.GroupVersionKind())
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ks), &updated); err != nil {
		t.Fatalf("failed to get resource: %v", err)
	}
	suspended, _, _ := unstructured.NestedBool(updated.Object, "spec", "suspend")
	if suspended {
		t.Errorf("spec.suspend should be false after timeout")
	}
}

func TestHandleFluxCDWarmup_CoLocatedUsesOwnNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	now := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)
	warmingUpSince := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)

	// HelmRelease lives in "team-a" with no spec.targetNamespace
	hr := newFluxHelmRelease("hr-app", "team-a", "", map[string]string{
		constants.ManagedByLabel: "dev-schedule",
		constants.StateLabel:     constants.StateWarmingUp,
	}, true)
	hr.SetAnnotations(map[string]string{constants.WarmingUpSinceAnnotation: warmingUpSince})

	// Deployment in "team-a" is ready
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(hr, deploy).
		WithStatusSubresource(deploy).
		Build()

	cfg := &lightsoutv1alpha1.FluxCDConfig{Namespace: "flux-system"}
	stillWarmingUp := handleFluxCDWarmup(context.Background(), fakeClient, nil, nil, cfg, "dev-schedule", []string{"team-a"}, now)
	if stillWarmingUp {
		t.Errorf("expected stillWarmingUp = false: co-located resource should use own namespace for readiness check")
	}
}
