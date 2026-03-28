package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

func ptr[T any](v T) *T {
	return &v
}

func TestScaleDeployment_Downscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		scheduleName   string
		wantReplicas   int32
		wantAnnotation string
		wantLabel      string
		wantSkipped    bool
	}{
		{
			name: "scale down deployment with 3 replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "dev"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
			},
			scheduleName:   "dev-schedule",
			wantReplicas:   0,
			wantAnnotation: "3",
			wantLabel:      "dev-schedule",
		},
		{
			name: "skip already scaled down deployment",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip deployment with 0 replicas (user-managed)",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "dev"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip deployment managed by different schedule",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.ManagedByAnnotation: "other-schedule",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 3,
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.deployment).
				Build()

			result, err := ScaleDeployment(context.Background(), fakeClient, tt.deployment, tt.scheduleName, false, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated appsv1.Deployment
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.deployment), &updated); err != nil {
				t.Fatalf("failed to get deployment: %v", err)
			}

			if *updated.Spec.Replicas != tt.wantReplicas {
				t.Errorf("replicas = %v, want %v", *updated.Spec.Replicas, tt.wantReplicas)
			}

			if !tt.wantSkipped && tt.wantAnnotation != "" {
				if updated.Annotations[constants.OriginalReplicasAnnotation] != tt.wantAnnotation {
					t.Errorf("annotation = %v, want %v", updated.Annotations[constants.OriginalReplicasAnnotation], tt.wantAnnotation)
				}
			}

			if !tt.wantSkipped && tt.wantLabel != "" {
				if updated.Labels[constants.ManagedByLabel] != tt.wantLabel {
					t.Errorf("label = %v, want %v", updated.Labels[constants.ManagedByLabel], tt.wantLabel)
				}
			}
		})
	}
}

func TestScaleStatefulSet_Downscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	tests := []struct {
		name           string
		statefulset    *appsv1.StatefulSet
		scheduleName   string
		wantReplicas   int32
		wantAnnotation string
		wantLabel      string
		wantSkipped    bool
	}{
		{
			name: "scale down statefulset with 3 replicas",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "dev"},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
			},
			scheduleName:   "dev-schedule",
			wantReplicas:   0,
			wantAnnotation: "3",
			wantLabel:      "dev-schedule",
		},
		{
			name: "skip already scaled down statefulset",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "db",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip statefulset with 0 replicas (user-managed)",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "dev"},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip statefulset managed by different schedule",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "db",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.ManagedByAnnotation: "other-schedule",
					},
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 3,
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.statefulset).
				Build()

			result, err := ScaleStatefulSet(context.Background(), fakeClient, tt.statefulset, tt.scheduleName, false, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated appsv1.StatefulSet
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.statefulset), &updated); err != nil {
				t.Fatalf("failed to get statefulset: %v", err)
			}

			if *updated.Spec.Replicas != tt.wantReplicas {
				t.Errorf("replicas = %v, want %v", *updated.Spec.Replicas, tt.wantReplicas)
			}

			if !tt.wantSkipped && tt.wantAnnotation != "" {
				if updated.Annotations[constants.OriginalReplicasAnnotation] != tt.wantAnnotation {
					t.Errorf("annotation = %v, want %v", updated.Annotations[constants.OriginalReplicasAnnotation], tt.wantAnnotation)
				}
			}

			if !tt.wantSkipped && tt.wantLabel != "" {
				if updated.Labels[constants.ManagedByLabel] != tt.wantLabel {
					t.Errorf("label = %v, want %v", updated.Labels[constants.ManagedByLabel], tt.wantLabel)
				}
			}
		})
	}
}

func TestScaleStatefulSet_Upscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	tests := []struct {
		name         string
		statefulset  *appsv1.StatefulSet
		scheduleName string
		wantReplicas int32
		wantSkipped  bool
	}{
		{
			name: "scale up statefulset from 0 to original",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "db",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 3,
		},
		{
			name: "skip statefulset without annotation (not managed by us)",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "dev"},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip statefulset managed by different schedule",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "db",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "other-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "other-schedule",
					},
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "handle invalid annotation value gracefully",
			statefulset: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "db",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "invalid",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.statefulset).
				Build()

			result, err := ScaleStatefulSet(context.Background(), fakeClient, tt.statefulset, tt.scheduleName, true, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated appsv1.StatefulSet
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.statefulset), &updated); err != nil {
				t.Fatalf("failed to get statefulset: %v", err)
			}

			if *updated.Spec.Replicas != tt.wantReplicas {
				t.Errorf("replicas = %v, want %v", *updated.Spec.Replicas, tt.wantReplicas)
			}

			if !tt.wantSkipped {
				if _, exists := updated.Annotations[constants.OriginalReplicasAnnotation]; exists {
					t.Errorf("original-replicas annotation should be removed after upscale")
				}
				if _, exists := updated.Labels[constants.ManagedByLabel]; exists {
					t.Errorf("managed-by label should be removed after upscale")
				}
			}
		})
	}
}

func TestScaleDeployment_Upscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	tests := []struct {
		name         string
		deployment   *appsv1.Deployment
		scheduleName string
		wantReplicas int32
		wantSkipped  bool
	}{
		{
			name: "scale up deployment from 0 to original",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 3,
		},
		{
			name: "skip deployment without annotation (not managed by us)",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "dev"},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "skip deployment managed by different schedule",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "3",
						constants.ManagedByAnnotation:        "other-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "other-schedule",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 0,
			wantSkipped:  true,
		},
		{
			name: "handle invalid annotation value gracefully",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "invalid",
						constants.ManagedByAnnotation:        "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
			},
			scheduleName: "dev-schedule",
			wantReplicas: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.deployment).
				Build()

			result, err := ScaleDeployment(context.Background(), fakeClient, tt.deployment, tt.scheduleName, true, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated appsv1.Deployment
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.deployment), &updated); err != nil {
				t.Fatalf("failed to get deployment: %v", err)
			}

			if *updated.Spec.Replicas != tt.wantReplicas {
				t.Errorf("replicas = %v, want %v", *updated.Spec.Replicas, tt.wantReplicas)
			}

			if !tt.wantSkipped {
				if _, exists := updated.Annotations[constants.OriginalReplicasAnnotation]; exists {
					t.Errorf("original-replicas annotation should be removed after upscale")
				}
				if _, exists := updated.Labels[constants.ManagedByLabel]; exists {
					t.Errorf("managed-by label should be removed after upscale")
				}
			}
		})
	}
}

func TestScaleCronJob_Downscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = batchv1.AddToScheme(scheme)

	tests := []struct {
		name           string
		cronjob        *batchv1.CronJob
		scheduleName   string
		wantSuspend    bool
		wantAnnotation string
		wantLabel      string
		wantSkipped    bool
	}{
		{
			name: "suspend active cronjob",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "dev"},
				Spec:       batchv1.CronJobSpec{Suspend: ptr(false)},
			},
			scheduleName:   "dev-schedule",
			wantSuspend:    true,
			wantAnnotation: constants.SuspendedByLightsOut,
			wantLabel:      "dev-schedule",
		},
		{
			name: "skip already suspended cronjob (user-managed)",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "dev"},
				Spec:       batchv1.CronJobSpec{Suspend: ptr(true)},
			},
			scheduleName: "dev-schedule",
			wantSuspend:  true,
			wantSkipped:  true,
		},
		{
			name: "skip already managed cronjob",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backup",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalSuspendAnnotation: constants.SuspendedByLightsOut,
						constants.ManagedByAnnotation:       "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: batchv1.CronJobSpec{Suspend: ptr(true)},
			},
			scheduleName: "dev-schedule",
			wantSuspend:  true,
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.cronjob).
				Build()

			result, err := ScaleCronJob(context.Background(), fakeClient, tt.cronjob, tt.scheduleName, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated batchv1.CronJob
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.cronjob), &updated); err != nil {
				t.Fatalf("failed to get cronjob: %v", err)
			}

			if *updated.Spec.Suspend != tt.wantSuspend {
				t.Errorf("suspend = %v, want %v", *updated.Spec.Suspend, tt.wantSuspend)
			}

			if !tt.wantSkipped && tt.wantAnnotation != "" {
				if updated.Annotations[constants.OriginalSuspendAnnotation] != tt.wantAnnotation {
					t.Errorf("annotation = %v, want %v", updated.Annotations[constants.OriginalSuspendAnnotation], tt.wantAnnotation)
				}
			}

			if !tt.wantSkipped && tt.wantLabel != "" {
				if updated.Labels[constants.ManagedByLabel] != tt.wantLabel {
					t.Errorf("label = %v, want %v", updated.Labels[constants.ManagedByLabel], tt.wantLabel)
				}
			}
		})
	}
}

func TestScaleCronJob_Upscale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = batchv1.AddToScheme(scheme)

	tests := []struct {
		name         string
		cronjob      *batchv1.CronJob
		scheduleName string
		wantSuspend  bool
		wantSkipped  bool
	}{
		{
			name: "resume lightsout-suspended cronjob",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backup",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalSuspendAnnotation: constants.SuspendedByLightsOut,
						constants.ManagedByAnnotation:       "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: batchv1.CronJobSpec{Suspend: ptr(true)},
			},
			scheduleName: "dev-schedule",
			wantSuspend:  false,
		},
		{
			name: "never resume user-suspended cronjob",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backup",
					Namespace: "dev",
					Annotations: map[string]string{
						constants.OriginalSuspendAnnotation: constants.SuspendedByUser,
						constants.ManagedByAnnotation:       "dev-schedule",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "dev-schedule",
					},
				},
				Spec: batchv1.CronJobSpec{Suspend: ptr(true)},
			},
			scheduleName: "dev-schedule",
			wantSuspend:  true,
			wantSkipped:  true,
		},
		{
			name: "mark suspended cronjob without annotation as user-owned",
			cronjob: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "dev"},
				Spec:       batchv1.CronJobSpec{Suspend: ptr(true)},
			},
			scheduleName: "dev-schedule",
			wantSuspend:  true,
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.cronjob).
				Build()

			result, err := ScaleCronJob(context.Background(), fakeClient, tt.cronjob, tt.scheduleName, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", result.Skipped, tt.wantSkipped)
			}

			var updated batchv1.CronJob
			if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.cronjob), &updated); err != nil {
				t.Fatalf("failed to get cronjob: %v", err)
			}

			if *updated.Spec.Suspend != tt.wantSuspend {
				t.Errorf("suspend = %v, want %v", *updated.Spec.Suspend, tt.wantSuspend)
			}

			if !tt.wantSkipped {
				if _, exists := updated.Labels[constants.ManagedByLabel]; exists {
					t.Errorf("managed-by label should be removed after upscale")
				}
			}
		})
	}
}

func TestScaleDeploymentDown_WithHPA(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-deploy", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}
	hpa := makeHPA("Deployment", "my-deploy", minReplicasPtr(2), nil)

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	addHPATypes(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	result, err := ScaleDeployment(context.Background(), fakeClient, deploy, "my-schedule", false, hpaList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Skipped {
		t.Fatal("expected deployment to be scaled, not skipped")
	}

	updated := &appsv1.Deployment{}
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-deploy"}, updated)
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Errorf("expected replicas=0 after downscale")
	}

	updatedHPA := &unstructured.Unstructured{}
	updatedHPA.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updatedHPA)
	// minReplicas should be unchanged (we disable scaleUp instead)
	minR, _, _ := unstructured.NestedInt64(updatedHPA.Object, "spec", "minReplicas")
	if minR != 2 {
		t.Errorf("expected HPA minReplicas unchanged at 2 after downscale, got %d", minR)
	}
	policy, _, _ := unstructured.NestedString(updatedHPA.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != hpaScaleUpDisabled {
		t.Errorf("expected scaleUp.selectPolicy=Disabled after downscale, got %q", policy)
	}
	if updatedHPA.GetAnnotations()[constants.ManagedByAnnotation] != "my-schedule" {
		t.Errorf("expected managed-by annotation set to my-schedule")
	}
}

func TestScaleDeploymentUp_WithHPA(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy",
			Namespace: "ns",
			Annotations: map[string]string{
				constants.OriginalReplicasAnnotation: "3",
				constants.ManagedByAnnotation:        "my-schedule",
			},
			Labels: map[string]string{constants.ManagedByLabel: "my-schedule"},
		},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0))},
	}
	// HPA in post-downscale state: scaleUp disabled, minReplicas unchanged at 2
	hpa := makeHPA("Deployment", "my-deploy", minReplicasPtr(2), map[string]string{
		constants.ManagedByAnnotation:                "my-schedule",
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	addHPATypes(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	result, err := ScaleDeployment(context.Background(), fakeClient, deploy, "my-schedule", true, hpaList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Skipped {
		t.Fatal("expected deployment to be scaled up, not skipped")
	}

	updated := &appsv1.Deployment{}
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-deploy"}, updated)
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 3 {
		t.Errorf("expected replicas=3 after upscale, got %v", updated.Spec.Replicas)
	}

	updatedHPA := &unstructured.Unstructured{}
	updatedHPA.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updatedHPA)
	// selectPolicy should be restored (removed)
	policy, found, _ := unstructured.NestedString(updatedHPA.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if found && policy != "" {
		t.Errorf("expected scaleUp.selectPolicy removed after upscale, got %q", policy)
	}
	if updatedHPA.GetAnnotations()[constants.ManagedByAnnotation] != "" {
		t.Error("expected managed-by annotation removed after upscale")
	}
	if updatedHPA.GetAnnotations()[constants.OriginalHPAScaleUpPolicyAnnotation] != "" {
		t.Error("expected original-hpa-scale-up-policy annotation removed after upscale")
	}
}

func TestScaleStatefulSetDown_WithHPA(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-sts", Namespace: "ns"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(2))},
	}
	hpa := makeHPA("StatefulSet", "my-sts", minReplicasPtr(2), nil)

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	addHPATypes(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	result, err := ScaleStatefulSet(context.Background(), fakeClient, sts, "my-schedule", false, hpaList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Skipped {
		t.Fatal("expected statefulset to be scaled, not skipped")
	}

	updatedHPA := &unstructured.Unstructured{}
	updatedHPA.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updatedHPA)
	// minReplicas should be unchanged (we disable scaleUp instead)
	minR, _, _ := unstructured.NestedInt64(updatedHPA.Object, "spec", "minReplicas")
	if minR != 2 {
		t.Errorf("expected HPA minReplicas unchanged at 2 after statefulset downscale, got %d", minR)
	}
	policy, _, _ := unstructured.NestedString(updatedHPA.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if policy != hpaScaleUpDisabled {
		t.Errorf("expected scaleUp.selectPolicy=Disabled after statefulset downscale, got %q", policy)
	}
}

func TestScaleStatefulSetUp_WithHPA(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-sts",
			Namespace: "ns",
			Annotations: map[string]string{
				constants.OriginalReplicasAnnotation: "2",
				constants.ManagedByAnnotation:        "my-schedule",
			},
			Labels: map[string]string{constants.ManagedByLabel: "my-schedule"},
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
	}
	// HPA in post-downscale state: scaleUp disabled, minReplicas unchanged at 2
	hpa := makeHPA("StatefulSet", "my-sts", minReplicasPtr(2), map[string]string{
		constants.ManagedByAnnotation:                "my-schedule",
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	addHPATypes(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).WithRuntimeObjects(hpa).Build()

	hpaList := mustListHPAs(t, fakeClient)
	result, err := ScaleStatefulSet(context.Background(), fakeClient, sts, "my-schedule", true, hpaList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Skipped {
		t.Fatal("expected statefulset to be scaled up, not skipped")
	}

	updatedHPA := &unstructured.Unstructured{}
	updatedHPA.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updatedHPA)
	// selectPolicy should be restored (removed) after upscale
	policy, found, _ := unstructured.NestedString(updatedHPA.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if found && policy != "" {
		t.Errorf("expected scaleUp.selectPolicy removed after statefulset upscale, got %q", policy)
	}
	if updatedHPA.GetAnnotations()[constants.ManagedByAnnotation] != "" {
		t.Error("expected managed-by annotation removed after statefulset upscale")
	}
}

// TestScaleDeploymentUp_HPACrashWindowRecovery verifies that if the controller crashed after
// restoring the workload but before RestoreHPA ran, a subsequent reconcile cleans up the HPA.
// In this state the deployment has no original-replicas annotation (already restored) but the
// HPA still has managed-by + original-hpa-scale-up-policy annotations and scaleUp=Disabled.
func TestScaleDeploymentUp_HPACrashWindowRecovery(t *testing.T) {
	// Simulate post-crash state: workload already restored (no lightsout annotations),
	// but HPA still has dangling annotations from the interrupted upscale.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-deploy", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}
	hpa := makeHPA("Deployment", "my-deploy", minReplicasPtr(2), map[string]string{
		constants.ManagedByAnnotation:                "my-schedule",
		constants.OriginalHPAScaleUpPolicyAnnotation: "",
	})
	_ = unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy")

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	addHPATypes(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).WithRuntimeObjects(hpa).Build()

	// ScaleDeployment with scaleUp=true - no original-replicas annotation, so workload is
	// skipped, but the HPA recovery path should still restore the selectPolicy.
	hpaList := mustListHPAs(t, fakeClient)
	result, err := ScaleDeployment(context.Background(), fakeClient, deploy, "my-schedule", true, hpaList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Skipped {
		t.Fatal("expected deployment to be skipped (already restored)")
	}

	updatedHPA := &unstructured.Unstructured{}
	updatedHPA.SetGroupVersionKind(schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"})
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "my-hpa"}, updatedHPA)

	policy, found, _ := unstructured.NestedString(updatedHPA.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if found && policy != "" {
		t.Errorf("expected scaleUp.selectPolicy restored (removed) during crash recovery, got %q", policy)
	}
	if updatedHPA.GetAnnotations()[constants.ManagedByAnnotation] != "" {
		t.Error("expected managed-by annotation removed from HPA after crash recovery")
	}
	if updatedHPA.GetAnnotations()[constants.OriginalHPAScaleUpPolicyAnnotation] != "" {
		t.Error("expected original-hpa-scale-up-policy annotation removed from HPA after crash recovery")
	}
}
