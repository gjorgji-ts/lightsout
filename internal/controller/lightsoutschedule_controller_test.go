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
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

func TestReconcile_WithRateLimiting(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	// Create 5 deployments
	var objects []client.Object
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: map[string]string{"env": "test"}}}
	objects = append(objects, ns)

	for i := 0; i < 5; i++ {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("deploy-%d", i), Namespace: "test-ns"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
		}
		objects = append(objects, deploy)
	}

	batchSize := 2
	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			Upscale:   "0 6 * * *",
			Downscale: "0 18 * * *",
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "test"},
			},
			DownscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: &batchSize,
			},
		},
	}
	objects = append(objects, schedule)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
		TimeFunc: func() time.Time {
			// Return a time during downscale period (7 PM)
			return time.Date(2025, 1, 15, 19, 0, 0, 0, time.UTC)
		},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-schedule"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Second reconcile to do the actual scaling (first one adds finalizer)
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-schedule"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify all deployments were scaled down
	var deployList appsv1.DeploymentList
	if err := fakeClient.List(context.Background(), &deployList, client.InNamespace("test-ns")); err != nil {
		t.Fatalf("failed to list deployments: %v", err)
	}

	for _, d := range deployList.Items {
		if *d.Spec.Replicas != 0 {
			t.Errorf("deployment %s should be scaled to 0, got %d", d.Name, *d.Spec.Replicas)
		}
	}
}

func TestCollectWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy1", Namespace: "ns1"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sts1", Namespace: "ns1"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(2))},
	}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "cj1", Namespace: "ns1"},
		Spec:       batchv1.CronJobSpec{Suspend: ptr(false)},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, sts, cj).
		Build()

	r := &LightsOutScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	spec := &lightsoutv1alpha1.LightsOutScheduleSpec{}
	workloads, err := r.collectWorkloads(context.Background(), []string{"ns1"}, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(workloads) != 3 {
		t.Errorf("expected 3 workloads, got %d", len(workloads))
	}
}

func TestProcessBatch(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy1", Namespace: "ns1"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy).
		Build()

	r := &LightsOutScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	batch := []Workload{WorkloadFromDeployment(deploy)}
	result := r.processBatch(context.Background(), batch, "test-schedule", false)

	if result.Processed != 1 {
		t.Errorf("expected 1 processed, got %d", result.Processed)
	}

	// Verify deployment was scaled down
	var updated appsv1.Deployment
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if *updated.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas, got %d", *updated.Spec.Replicas)
	}
}

var _ = Describe("LightsOutSchedule Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	// Use a fixed time for testing (10 AM UTC)
	var testTime = time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	Context("When creating a Schedule", func() {
		It("Should scale down deployments when in down period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-down",
					Labels: map[string]string{
						"environment": "test",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			// Cleanup resources after test
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment with 3 replicas
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy",
					Namespace: "test-ns-down",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule that is in "down" period
			// At 10 AM: lastDownscale (6 AM today) > lastUpscale (6 PM yesterday) = "Down"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-down",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *", // 6 PM daily
					Downscale: "0 6 * * *",  // 6 AM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-down"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-down"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was scaled down
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy",
					Namespace: "test-ns-down",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			// Verify annotations were added
			Expect(updatedDeploy.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))
			Expect(updatedDeploy.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-down"))
			// Verify label was added
			Expect(updatedDeploy.Labels[constants.ManagedByLabel]).To(Equal("test-schedule-down"))
		})

		It("Should scale up deployments when in up period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-up",
					Labels: map[string]string{
						"environment": "test-up",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment that was previously scaled down (0 replicas with annotations)
			zero := int32(0)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-up",
					Namespace: "test-ns-up",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "5",
						constants.ManagedByAnnotation:        "test-schedule-up",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "test-schedule-up",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &zero,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-up"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-up"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule that is in "up" period
			// At 10 AM: lastUpscale (6 AM today) > lastDownscale (6 PM yesterday) = "Up"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-up",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 6 * * *",  // 6 AM daily
					Downscale: "0 18 * * *", // 6 PM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-up"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-up"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-up"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was scaled up
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-up",
					Namespace: "test-ns-up",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(5)))

			// Verify annotations were removed
			Expect(updatedDeploy.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(updatedDeploy.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))
			// Verify label was removed
			Expect(updatedDeploy.Labels).NotTo(HaveKey(constants.ManagedByLabel))
		})

		It("Should skip reconciliation when schedule is suspended", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-suspended",
					Labels: map[string]string{
						"environment": "test-suspended",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment with 2 replicas
			replicas := int32(2)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-suspended",
					Namespace: "test-ns-suspended",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-suspended"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-suspended"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule that is suspended (even though it would be in "down" period)
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-suspended",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 23 * * *", // 11 PM daily
					Downscale: "0 6 * * *",  // 6 AM daily - would be in "down" period
					Timezone:  "UTC",
					Suspend:   true, // Schedule is suspended
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-suspended"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-suspended"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile skips due to suspend
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-suspended"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was NOT scaled (still has 2 replicas)
			var updatedDeploy appsv1.Deployment
			Consistently(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-suspended",
					Namespace: "test-ns-suspended",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, time.Second*2, interval).Should(Equal(int32(2)))

			// Verify no annotations were added
			Expect(updatedDeploy.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(updatedDeploy.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))
		})

		It("Should scale down StatefulSets when in down period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-sts-down",
					Labels: map[string]string{
						"environment": "test-sts",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create StatefulSet with 3 replicas
			replicas := int32(3)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "test-ns-sts-down",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &replicas,
					ServiceName: "test-sts-svc",
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-sts"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-sts"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, sts)
			})

			// Create schedule that is in "down" period
			// At 10 AM: lastDownscale (6 AM today) > lastUpscale (6 PM yesterday) = "Down"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-sts-down",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *", // 6 PM daily
					Downscale: "0 6 * * *",  // 6 AM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-sts"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-sts-down"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-sts-down"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify StatefulSet was scaled down
			var updatedSts appsv1.StatefulSet
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sts",
					Namespace: "test-ns-sts-down",
				}, &updatedSts)
				if err != nil {
					return -1
				}
				return *updatedSts.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			// Verify annotations were added
			Expect(updatedSts.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))
			Expect(updatedSts.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-sts-down"))
			// Verify label was added
			Expect(updatedSts.Labels[constants.ManagedByLabel]).To(Equal("test-schedule-sts-down"))
		})

		It("Should scale up StatefulSets when in up period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-sts-up",
					Labels: map[string]string{
						"environment": "test-sts-up",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create StatefulSet that was previously scaled down (0 replicas with annotations)
			zero := int32(0)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts-up",
					Namespace: "test-ns-sts-up",
					Annotations: map[string]string{
						constants.OriginalReplicasAnnotation: "5",
						constants.ManagedByAnnotation:        "test-schedule-sts-up",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "test-schedule-sts-up",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &zero,
					ServiceName: "test-sts-up-svc",
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-sts-up"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-sts-up"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, sts)
			})

			// Create schedule that is in "up" period
			// At 10 AM: lastUpscale (6 AM today) > lastDownscale (6 PM yesterday) = "Up"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-sts-up",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 6 * * *",  // 6 AM daily
					Downscale: "0 18 * * *", // 6 PM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-sts-up"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-sts-up"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-sts-up"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify StatefulSet was scaled up
			var updatedSts appsv1.StatefulSet
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sts-up",
					Namespace: "test-ns-sts-up",
				}, &updatedSts)
				if err != nil {
					return -1
				}
				return *updatedSts.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(5)))

			// Verify annotations were removed
			Expect(updatedSts.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(updatedSts.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))
			// Verify label was removed
			Expect(updatedSts.Labels).NotTo(HaveKey(constants.ManagedByLabel))
		})

		It("Should suspend CronJobs when in down period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-cj-down",
					Labels: map[string]string{
						"environment": "test-cj",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create CronJob that is not suspended
			suspend := false
			cj := &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cj",
					Namespace: "test-ns-cj-down",
				},
				Spec: batchv1.CronJobSpec{
					Schedule: "*/5 * * * *",
					Suspend:  &suspend,
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:    "test",
											Image:   "busybox",
											Command: []string{"echo", "hello"},
										},
									},
									RestartPolicy: corev1.RestartPolicyOnFailure,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cj)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, cj)
			})

			// Create schedule that is in "down" period
			// At 10 AM: lastDownscale (6 AM today) > lastUpscale (6 PM yesterday) = "Down"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-cj-down",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *", // 6 PM daily
					Downscale: "0 6 * * *",  // 6 AM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-cj"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-cj-down"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-cj-down"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify CronJob was suspended
			var updatedCJ batchv1.CronJob
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-cj",
					Namespace: "test-ns-cj-down",
				}, &updatedCJ)
				if err != nil {
					return false
				}
				return updatedCJ.Spec.Suspend != nil && *updatedCJ.Spec.Suspend
			}, timeout, interval).Should(BeTrue())

			// Verify annotations were added
			Expect(updatedCJ.Annotations[constants.OriginalSuspendAnnotation]).To(Equal(constants.SuspendedByLightsOut))
			Expect(updatedCJ.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-cj-down"))
			// Verify label was added
			Expect(updatedCJ.Labels[constants.ManagedByLabel]).To(Equal("test-schedule-cj-down"))
		})

		It("Should resume CronJobs when in up period", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-cj-up",
					Labels: map[string]string{
						"environment": "test-cj-up",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create CronJob that was previously suspended by LightsOut
			suspend := true
			cj := &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cj-up",
					Namespace: "test-ns-cj-up",
					Annotations: map[string]string{
						constants.OriginalSuspendAnnotation: constants.SuspendedByLightsOut,
						constants.ManagedByAnnotation:       "test-schedule-cj-up",
					},
					Labels: map[string]string{
						constants.ManagedByLabel: "test-schedule-cj-up",
					},
				},
				Spec: batchv1.CronJobSpec{
					Schedule: "*/5 * * * *",
					Suspend:  &suspend,
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:    "test",
											Image:   "busybox",
											Command: []string{"echo", "hello"},
										},
									},
									RestartPolicy: corev1.RestartPolicyOnFailure,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cj)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, cj)
			})

			// Create schedule that is in "up" period
			// At 10 AM: lastUpscale (6 AM today) > lastDownscale (6 PM yesterday) = "Up"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-cj-up",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 6 * * *",  // 6 AM daily
					Downscale: "0 18 * * *", // 6 PM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-cj-up"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-cj-up"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-cj-up"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify CronJob was resumed
			var updatedCJ batchv1.CronJob
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-cj-up",
					Namespace: "test-ns-cj-up",
				}, &updatedCJ)
				if err != nil {
					return true // return true to fail the assertion if error
				}
				return updatedCJ.Spec.Suspend != nil && *updatedCJ.Spec.Suspend
			}, timeout, interval).Should(BeFalse())

			// Verify annotations were removed
			Expect(updatedCJ.Annotations).NotTo(HaveKey(constants.OriginalSuspendAnnotation))
			Expect(updatedCJ.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))
			// Verify label was removed
			Expect(updatedCJ.Labels).NotTo(HaveKey(constants.ManagedByLabel))
		})

		It("Should handle multiple workload types in one namespace", func() {
			ctx := context.Background()

			// Create namespace with label
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-multi",
					Labels: map[string]string{
						"environment": "test-multi",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create Deployment with 2 replicas
			deployReplicas := int32(2)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multi-deploy",
					Namespace: "test-ns-multi",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &deployReplicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-multi-deploy"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-multi-deploy"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create StatefulSet with 3 replicas
			stsReplicas := int32(3)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multi-sts",
					Namespace: "test-ns-multi",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &stsReplicas,
					ServiceName: "test-multi-sts-svc",
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-multi-sts"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-multi-sts"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, sts)
			})

			// Create CronJob that is not suspended
			suspend := false
			cj := &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multi-cj",
					Namespace: "test-ns-multi",
				},
				Spec: batchv1.CronJobSpec{
					Schedule: "*/10 * * * *",
					Suspend:  &suspend,
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:    "test",
											Image:   "busybox",
											Command: []string{"echo", "hello"},
										},
									},
									RestartPolicy: corev1.RestartPolicyOnFailure,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cj)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, cj)
			})

			// Create schedule that is in "down" period
			// At 10 AM: lastDownscale (6 AM today) > lastUpscale (6 PM yesterday) = "Down"
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-multi",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *", // 6 PM daily
					Downscale: "0 6 * * *",  // 6 AM daily
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-multi"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Manually trigger reconciliation
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-multi"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile does the actual work
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-multi"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Deployment was scaled down
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-multi-deploy",
					Namespace: "test-ns-multi",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(updatedDeploy.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("2"))
			Expect(updatedDeploy.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-multi"))

			// Verify StatefulSet was scaled down
			var updatedSts appsv1.StatefulSet
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-multi-sts",
					Namespace: "test-ns-multi",
				}, &updatedSts)
				if err != nil {
					return -1
				}
				return *updatedSts.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(updatedSts.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))
			Expect(updatedSts.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-multi"))

			// Verify CronJob was suspended
			var updatedCJ batchv1.CronJob
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-multi-cj",
					Namespace: "test-ns-multi",
				}, &updatedCJ)
				if err != nil {
					return false
				}
				return updatedCJ.Spec.Suspend != nil && *updatedCJ.Spec.Suspend
			}, timeout, interval).Should(BeTrue())
			Expect(updatedCJ.Annotations[constants.OriginalSuspendAnnotation]).To(Equal(constants.SuspendedByLightsOut))
			Expect(updatedCJ.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-multi"))
		})
	})

	Context("Finalizer behavior", func() {
		It("Should add finalizer to schedule on first reconcile", func() {
			ctx := context.Background()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-finalizer",
					Labels: map[string]string{
						"environment": "test-finalizer",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create schedule without finalizer
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-finalizer",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 8 * * *",
					Downscale: "0 18 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-finalizer"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Reconcile once - should add finalizer
			reconciler := &LightsOutScheduleReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-finalizer"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			// Verify finalizer was added
			var updatedSchedule lightsoutv1alpha1.LightsOutSchedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-finalizer"}, &updatedSchedule)).Should(Succeed())
			Expect(updatedSchedule.Finalizers).To(ContainElement(constants.FinalizerName))
		})

		It("Should restore workloads when schedule is deleted", func() {
			ctx := context.Background()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-deletion",
					Labels: map[string]string{
						"environment": "test-deletion",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment with 3 replicas
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-deletion",
					Namespace: "test-ns-deletion",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-deletion"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-deletion"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule in down period (10 AM, downscale at 6 AM, upscale at 6 PM)
			testTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-deletion",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-deletion"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			// Reconcile to add finalizer and scale down
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}
			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deletion"},
			})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile scales down
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deletion"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was scaled down
			var scaledDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-deletion",
					Namespace: "test-ns-deletion",
				}, &scaledDeploy)
				if err != nil {
					return -1
				}
				return *scaledDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(scaledDeploy.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))

			// Delete the schedule
			Expect(k8sClient.Delete(ctx, schedule)).Should(Succeed())

			// Reconcile to handle deletion
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deletion"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was restored
			var restoredDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-deletion",
					Namespace: "test-ns-deletion",
				}, &restoredDeploy)
				if err != nil {
					return -1
				}
				return *restoredDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(3)))

			// Verify annotations were removed
			Expect(restoredDeploy.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(restoredDeploy.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))

			// Verify schedule was deleted (finalizer removed)
			var deletedSchedule lightsoutv1alpha1.LightsOutSchedule
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-deletion"}, &deletedSchedule)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("State transitions over time", func() {
		It("Should transition from up to down and back as time progresses", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-transition",
					Labels: map[string]string{
						"environment": "test-transition",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment with 3 replicas
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-transition",
					Namespace: "test-ns-transition",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-transition"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-transition"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Schedule: upscale at 6 AM, downscale at 6 PM
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-transition",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 6 * * *",
					Downscale: "0 18 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-transition"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Time 1: 10 AM (Up period - between 6 AM upscale and 6 PM downscale)
			morningTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return morningTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// Reconcile twice (first adds finalizer)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-transition"},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-transition"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment still has 3 replicas (Up period)
			var updatedDeploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-deploy-transition",
				Namespace: "test-ns-transition",
			}, &updatedDeploy)).Should(Succeed())
			Expect(*updatedDeploy.Spec.Replicas).To(Equal(int32(3)))

			// Time 2: 7 PM (Down period - after 6 PM downscale)
			eveningTime := time.Date(2024, 1, 15, 19, 0, 0, 0, time.UTC)
			reconciler.TimeFunc = func() time.Time { return eveningTime }
			ClearPeriodCache() // Clear cache so period is recalculated with new time

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-transition"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment scaled down to 0
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-transition",
					Namespace: "test-ns-transition",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(updatedDeploy.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))

			// Time 3: 7 AM next day (Up period - after 6 AM upscale)
			nextMorningTime := time.Date(2024, 1, 16, 7, 0, 0, 0, time.UTC)
			reconciler.TimeFunc = func() time.Time { return nextMorningTime }
			ClearPeriodCache() // Clear cache so period is recalculated with new time

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-transition"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment scaled back up to 3
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-transition",
					Namespace: "test-ns-transition",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(3)))
			Expect(updatedDeploy.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
		})
	})

	Context("Multiple schedules with overlapping namespaces", func() {
		It("Should not allow second schedule to take over workloads managed by first", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-overlap",
					Labels: map[string]string{
						"environment": "test-overlap",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-overlap",
					Namespace: "test-ns-overlap",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-overlap"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-overlap"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create first schedule (in down period at 10 AM)
			schedule1 := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-overlap-1",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-overlap"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule1)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule1)
			})

			testTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// Reconcile first schedule
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-overlap-1"},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-overlap-1"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment scaled down and managed by schedule-1
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-overlap",
					Namespace: "test-ns-overlap",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(updatedDeploy.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-overlap-1"))

			// Create second schedule targeting same namespace
			schedule2 := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-overlap-2",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-overlap"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule2)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule2)
			})

			// Reconcile second schedule
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-overlap-2"},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-overlap-2"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment is still managed by schedule-1 (not taken over)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-deploy-overlap",
				Namespace: "test-ns-overlap",
			}, &updatedDeploy)).Should(Succeed())
			Expect(updatedDeploy.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-overlap-1"))
		})
	})

	Context("Edge cases", func() {
		It("Should handle deployment deleted while managed", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-deleted-deploy",
					Labels: map[string]string{
						"environment": "test-deleted-deploy",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment
			replicas := int32(2)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-to-delete",
					Namespace: "test-ns-deleted-deploy",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-delete"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-delete"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			// Create schedule in down period
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-deleted-deploy",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-deleted-deploy"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			testTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// Reconcile to scale down
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deleted-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deleted-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment is scaled down
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-to-delete",
					Namespace: "test-ns-deleted-deploy",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			// Delete the deployment while it's managed
			Expect(k8sClient.Delete(ctx, deploy)).Should(Succeed())

			// Reconcile again - should handle missing deployment gracefully
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-deleted-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify schedule still reconciles without error
			var updatedSchedule lightsoutv1alpha1.LightsOutSchedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-deleted-deploy"}, &updatedSchedule)).Should(Succeed())
		})

		It("Should handle multiple rapid reconciliations idempotently", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-rapid",
					Labels: map[string]string{
						"environment": "test-rapid",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment
			replicas := int32(5)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-rapid",
					Namespace: "test-ns-rapid",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-rapid"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-rapid"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule in down period
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-rapid",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-rapid"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			testTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// Rapid reconciliations (10 times)
			for i := 0; i < 10; i++ {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "test-schedule-rapid"},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify deployment is scaled down with correct original replicas
			var updatedDeploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-deploy-rapid",
				Namespace: "test-ns-rapid",
			}, &updatedDeploy)).Should(Succeed())
			Expect(*updatedDeploy.Spec.Replicas).To(Equal(int32(0)))
			// Original replicas should still be 5 (not overwritten by subsequent reconciles)
			Expect(updatedDeploy.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("5"))
		})

		It("Should freeze state when schedule suspended mid-transition", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-mid-suspend",
					Labels: map[string]string{
						"environment": "test-mid-suspend",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create deployment
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-mid-suspend",
					Namespace: "test-ns-mid-suspend",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-mid-suspend"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-mid-suspend"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy)
			})

			// Create schedule in down period
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-mid-suspend",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-mid-suspend"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, schedule)
			})

			// Time during down period
			downTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return downTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// Reconcile to scale down
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-mid-suspend"},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-mid-suspend"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify scaled down
			var updatedDeploy appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-mid-suspend",
					Namespace: "test-ns-mid-suspend",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			// Suspend the schedule
			var currentSchedule lightsoutv1alpha1.LightsOutSchedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-mid-suspend"}, &currentSchedule)).Should(Succeed())
			currentSchedule.Spec.Suspend = true
			Expect(k8sClient.Update(ctx, &currentSchedule)).Should(Succeed())

			// Change time to up period
			upTime := time.Date(2024, 1, 15, 19, 0, 0, 0, time.UTC)
			reconciler.TimeFunc = func() time.Time { return upTime }

			// Reconcile - should skip because suspended
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-mid-suspend"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment stays at 0 (frozen state)
			Consistently(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-mid-suspend",
					Namespace: "test-ns-mid-suspend",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, time.Second*2, interval).Should(Equal(int32(0)))

			// Unsuspend the schedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-mid-suspend"}, &currentSchedule)).Should(Succeed())
			currentSchedule.Spec.Suspend = false
			Expect(k8sClient.Update(ctx, &currentSchedule)).Should(Succeed())

			// Reconcile - should now scale up
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-mid-suspend"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment scaled back up
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-mid-suspend",
					Namespace: "test-ns-mid-suspend",
				}, &updatedDeploy)
				if err != nil {
					return -1
				}
				return *updatedDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(3)))
		})
	})

	Context("Multi-deployment deletion cleanup", func() {
		It("Should restore all deployments when schedule is deleted", func() {
			ctx := context.Background()

			// Clear the period cache to ensure our mocked time is used
			ClearPeriodCache()

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-multi-deploy",
					Labels: map[string]string{
						"environment": "test-multi-deploy",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, ns)
			})

			// Create two deployments with different replica counts
			replicas2 := int32(2)
			deploy1 := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-multi-1",
					Namespace: "test-ns-multi-deploy",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas2,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-multi-1"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-multi-1"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy1)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy1)
			})

			replicas3 := int32(3)
			deploy2 := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy-multi-2",
					Namespace: "test-ns-multi-deploy",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas3,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test-multi-2"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test-multi-2"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy2)).Should(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, deploy2)
			})

			// Create schedule in down period
			testTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			schedule := &lightsoutv1alpha1.LightsOutSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-schedule-multi-deploy",
				},
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Upscale:   "0 18 * * *",
					Downscale: "0 6 * * *",
					Timezone:  "UTC",
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"environment": "test-multi-deploy"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schedule)).Should(Succeed())

			// Reconcile to add finalizer and scale down
			reconciler := &LightsOutScheduleReconciler{
				TimeFunc: func() time.Time { return testTime },
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
			}

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-multi-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile scales down
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-multi-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify both deployments were scaled down
			var scaledDeploy1, scaledDeploy2 appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-multi-1",
					Namespace: "test-ns-multi-deploy",
				}, &scaledDeploy1)
				if err != nil {
					return -1
				}
				return *scaledDeploy1.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(scaledDeploy1.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("2"))
			Expect(scaledDeploy1.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-multi-deploy"))

			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-multi-2",
					Namespace: "test-ns-multi-deploy",
				}, &scaledDeploy2)
				if err != nil {
					return -1
				}
				return *scaledDeploy2.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))
			Expect(scaledDeploy2.Annotations[constants.OriginalReplicasAnnotation]).To(Equal("3"))
			Expect(scaledDeploy2.Annotations[constants.ManagedByAnnotation]).To(Equal("test-schedule-multi-deploy"))

			// Delete the schedule
			Expect(k8sClient.Delete(ctx, schedule)).Should(Succeed())

			// Reconcile to handle deletion
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-schedule-multi-deploy"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify BOTH deployments were restored
			var restoredDeploy1, restoredDeploy2 appsv1.Deployment
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-multi-1",
					Namespace: "test-ns-multi-deploy",
				}, &restoredDeploy1)
				if err != nil {
					return -1
				}
				return *restoredDeploy1.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(2)))
			Expect(restoredDeploy1.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(restoredDeploy1.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))

			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-deploy-multi-2",
					Namespace: "test-ns-multi-deploy",
				}, &restoredDeploy2)
				if err != nil {
					return -1
				}
				return *restoredDeploy2.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(3)))
			Expect(restoredDeploy2.Annotations).NotTo(HaveKey(constants.OriginalReplicasAnnotation))
			Expect(restoredDeploy2.Annotations).NotTo(HaveKey(constants.ManagedByAnnotation))

			// Verify schedule was deleted (finalizer removed)
			var deletedSchedule lightsoutv1alpha1.LightsOutSchedule
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "test-schedule-multi-deploy"}, &deletedSchedule)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})
