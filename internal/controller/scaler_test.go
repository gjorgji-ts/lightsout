package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

			result, err := ScaleDeployment(context.Background(), fakeClient, tt.deployment, tt.scheduleName, false)
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

			result, err := ScaleStatefulSet(context.Background(), fakeClient, tt.statefulset, tt.scheduleName, false)
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

			result, err := ScaleStatefulSet(context.Background(), fakeClient, tt.statefulset, tt.scheduleName, true)
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

			result, err := ScaleDeployment(context.Background(), fakeClient, tt.deployment, tt.scheduleName, true)
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
