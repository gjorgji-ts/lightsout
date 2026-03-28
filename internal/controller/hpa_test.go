package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// mustListHPAs is a test helper that calls listHPAs and fails the test on error.
func mustListHPAs(t *testing.T, c client.Client) *unstructured.UnstructuredList {
	t.Helper()
	list, err := listHPAs(context.Background(), c, "ns")
	if err != nil {
		t.Fatalf("listHPAs: %v", err)
	}
	return list
}

func TestPatchHPAForDownscale_HPAFound(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, nil)
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated); err != nil {
		t.Fatalf("failed to get updated HPA: %v", err)
	}

	policy, _, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != hpaScaleUpDisabled {
		t.Errorf("expected scaleUp.selectPolicy=Disabled, got %q", policy)
	}
	annotations := updated.GetAnnotations()
	if annotations[constants.OriginalHPAScaleUpPolicyAnnotation] != "" {
		t.Errorf("expected original-hpa-scale-up-policy='' (absent), got %q", annotations[constants.OriginalHPAScaleUpPolicyAnnotation])
	}
	if annotations[constants.ManagedByAnnotation] != testScheduleName {
		t.Errorf("expected managed-by=my-schedule, got %q", annotations[constants.ManagedByAnnotation])
	}
}

func TestPatchHPAForDownscale_NoHPA(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).Build()
	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("expected no error when no HPA found, got: %v", err)
	}
}

func TestPatchHPAForDownscale_UserManagedDisabled(t *testing.T) {
	// User has already disabled scale-up - LightsOut should not touch this HPA
	hpa := makeHPA("Deployment", "my-deploy", nil, nil)
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	if updated.GetAnnotations()[constants.ManagedByAnnotation] != "" {
		t.Error("expected HPA to be skipped (no managed-by stamped)")
	}
}

func TestPatchHPAForDownscale_DifferentSchedule(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation:                "other-schedule",
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	if updated.GetAnnotations()[constants.ManagedByAnnotation] != "other-schedule" {
		t.Error("expected HPA ownership to remain with other-schedule")
	}
}

func TestPatchHPAForDownscale_AlreadyPatched(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation:                testScheduleName,
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second call should be idempotent - managed-by stays, no double-patch
	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	if updated.GetAnnotations()[constants.ManagedByAnnotation] != testScheduleName {
		t.Errorf("expected managed-by to remain my-schedule")
	}
}

func TestPatchHPAForDownscale_PreservesExistingPolicy(t *testing.T) {
	// HPA had "Min" selectPolicy set by user - we should store it and set Disabled
	hpa := makeHPA("Deployment", "my-deploy", nil, nil)
	_ = unstructured.SetNestedField(hpa.Object, "Min", "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := PatchHPAForDownscale(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)

	policy, _, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != hpaScaleUpDisabled {
		t.Errorf("expected scaleUp.selectPolicy=Disabled, got %q", policy)
	}
	if updated.GetAnnotations()[constants.OriginalHPAScaleUpPolicyAnnotation] != "Min" {
		t.Errorf("expected original-hpa-scale-up-policy=Min, got %q", updated.GetAnnotations()[constants.OriginalHPAScaleUpPolicyAnnotation])
	}
}

func TestRestoreHPA_Success(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation:                testScheduleName,
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)

	// selectPolicy should be absent (restored to default)
	policy, found, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if found && policy != "" {
		t.Errorf("expected selectPolicy to be removed, got %q", policy)
	}
	annotations := updated.GetAnnotations()
	if annotations[constants.OriginalHPAScaleUpPolicyAnnotation] != "" {
		t.Error("expected original-hpa-scale-up-policy annotation to be removed")
	}
	if annotations[constants.ManagedByAnnotation] != "" {
		t.Error("expected managed-by annotation to be removed")
	}
}

func TestRestoreHPA_NoHPA(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("expected no error when no HPA found, got: %v", err)
	}
}

func TestRestoreHPA_NoManagedByAnnotation(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, nil)
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	// selectPolicy should be unchanged (absent)
	_, found, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if found {
		t.Error("expected selectPolicy to remain absent")
	}
}

func TestRestoreHPA_DifferentSchedule(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation:                "other-schedule",
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	if updated.GetAnnotations()[constants.ManagedByAnnotation] != "other-schedule" {
		t.Error("expected ownership to remain with other-schedule")
	}
}

func TestRestoreHPA_MissingPolicyAnnotation(t *testing.T) {
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation: testScheduleName,
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("expected no error (warning only), got: %v", err)
	}

	// selectPolicy should remain unchanged (no restore attempted)
	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	policy, _, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != hpaScaleUpDisabled {
		t.Errorf("expected selectPolicy unchanged at Disabled when annotation absent, got %q", policy)
	}
}

func TestRestoreHPA_RestoresNonEmptyPolicy(t *testing.T) {
	// Original policy was "Min" - should be restored
	hpa := makeHPA("Deployment", "my-deploy", nil, map[string]string{
		constants.ManagedByAnnotation:                testScheduleName,
		constants.OriginalHPAScaleUpPolicyAnnotation: "Min",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")
	fakeClient := fake.NewClientBuilder().WithScheme(hpaScheme()).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	err := RestoreHPA(context.Background(), fakeClient, hpaList, "ns", "Deployment", "my-deploy", testScheduleName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updated)
	policy, _, _ := unstructured.NestedString(updated.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != "Min" {
		t.Errorf("expected selectPolicy=Min after restore, got %q", policy)
	}
	if updated.GetAnnotations()[constants.OriginalHPAScaleUpPolicyAnnotation] != "" {
		t.Error("expected original-hpa-scale-up-policy annotation to be removed")
	}
}
