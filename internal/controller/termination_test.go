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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// terminatingPod builds a pod that started terminating deletedAgo before now.
// A zero deletedAgo leaves it running.
func terminatingPod(name, namespace string, now time.Time, deletedAgo, grace time.Duration) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{"lightsout.test/hold"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if deletedAgo > 0 {
		deleted := metav1.NewTime(now.Add(-deletedAgo))
		pod.DeletionTimestamp = &deleted
		seconds := int64(grace.Seconds())
		pod.DeletionGracePeriodSeconds = &seconds
	}
	return pod
}

func TestCountTerminatingPods(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	now := time.Date(2026, 3, 21, 22, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		pods            []*corev1.Pod
		namespaces      []string
		wantTerminating int
		wantStuck       int
		wantNames       []string
	}{
		{
			name:       "running pods are not counted",
			pods:       []*corev1.Pod{terminatingPod("web", "dev", now, 0, 0)},
			namespaces: []string{"dev"},
		},
		{
			// A pod inside its grace period is shutting down normally. Alerting on
			// it would fire on every downscale.
			name:            "pod within its grace period is terminating but not stuck",
			pods:            []*corev1.Pod{terminatingPod("web", "dev", now, 10*time.Second, 30*time.Second)},
			namespaces:      []string{"dev"},
			wantTerminating: 1,
		},
		{
			// Past grace, but still inside the slack the controller allows for a
			// slow node.
			name:            "pod just past its grace period is not yet stuck",
			pods:            []*corev1.Pod{terminatingPod("web", "dev", now, 45*time.Second, 30*time.Second)},
			namespaces:      []string{"dev"},
			wantTerminating: 1,
		},
		{
			name:            "pod past grace period plus slack is stuck",
			pods:            []*corev1.Pod{terminatingPod("web", "dev", now, 30*time.Minute, 30*time.Second)},
			namespaces:      []string{"dev"},
			wantTerminating: 1,
			wantStuck:       1,
			wantNames:       []string{"dev/web"},
		},
		{
			// A long preStop hook gets judged against its own budget, not a fixed
			// number, so a legitimate slow drain is not reported.
			name: "long grace period is respected",
			pods: []*corev1.Pod{
				terminatingPod("drain", "dev", now, 10*time.Minute, 30*time.Minute),
			},
			namespaces:      []string{"dev"},
			wantTerminating: 1,
		},
		{
			name: "finished pods hold no compute",
			pods: func() []*corev1.Pod {
				p := terminatingPod("job", "dev", now, 30*time.Minute, 30*time.Second)
				p.Status.Phase = corev1.PodSucceeded
				return []*corev1.Pod{p}
			}(),
			namespaces: []string{"dev"},
		},
		{
			// A pod whose containers were killed reaches Failed, and its compute is
			// already back. Only a pod the kubelet could not kill stays Running.
			// An e2e run proved this matters: a pod deleted with a zero grace period
			// exits 137, lands in Failed, and must not be reported.
			name: "a pod whose containers died is not stuck",
			pods: func() []*corev1.Pod {
				p := terminatingPod("web", "dev", now, 30*time.Minute, 0)
				p.Status.Phase = corev1.PodFailed
				return []*corev1.Pod{p}
			}(),
			namespaces: []string{"dev"},
		},
		{
			// A pod that never got a node has no container to exit, so it stays
			// Pending for as long as something holds the object.
			name: "an unscheduled pod that will not go away is stuck",
			pods: func() []*corev1.Pod {
				p := terminatingPod("web", "dev", now, 30*time.Minute, 0)
				p.Status.Phase = corev1.PodPending
				return []*corev1.Pod{p}
			}(),
			namespaces:      []string{"dev"},
			wantTerminating: 1,
			wantStuck:       1,
			wantNames:       []string{"dev/web"},
		},
		{
			name: "stuck pods are found across namespaces and sorted",
			pods: []*corev1.Pod{
				terminatingPod("api", "staging", now, time.Hour, 30*time.Second),
				terminatingPod("web", "dev", now, time.Hour, 30*time.Second),
			},
			namespaces:      []string{"dev", "staging"},
			wantTerminating: 2,
			wantStuck:       2,
			wantNames:       []string{"dev/web", "staging/api"},
		},
		{
			name:            "pods outside the managed namespaces are ignored",
			pods:            []*corev1.Pod{terminatingPod("web", "other", now, time.Hour, 30*time.Second)},
			namespaces:      []string{"dev"},
			wantTerminating: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.pods))
			for _, p := range tt.pods {
				objs = append(objs, p)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			report, err := countTerminatingPods(context.Background(), fakeClient, tt.namespaces, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if report.Terminating != tt.wantTerminating {
				t.Errorf("terminating = %d, want %d", report.Terminating, tt.wantTerminating)
			}
			if report.Stuck != tt.wantStuck {
				t.Errorf("stuck = %d, want %d", report.Stuck, tt.wantStuck)
			}
			if len(report.StuckNames) != len(tt.wantNames) {
				t.Fatalf("stuck names = %v, want %v", report.StuckNames, tt.wantNames)
			}
			for i, want := range tt.wantNames {
				if report.StuckNames[i] != want {
					t.Errorf("stuck name %d = %q, want %q", i, report.StuckNames[i], want)
				}
			}
			// Every managed namespace gets an entry so the gauge can be reset.
			for _, ns := range tt.namespaces {
				if _, ok := report.StuckByNamespace[ns]; !ok {
					t.Errorf("namespace %q missing from the per-namespace counts", ns)
				}
			}
		})
	}
}

func TestApplyTerminationRequeue(t *testing.T) {
	tests := []struct {
		name           string
		requeue        time.Duration
		report         terminationReport
		justScaledDown bool
		expected       time.Duration
	}{
		{
			// Nothing terminating and nothing just scaled: the schedule sleeps
			// until the next transition.
			name:     "a settled namespace leaves the requeue alone",
			requeue:  8 * time.Hour,
			report:   terminationReport{},
			expected: 8 * time.Hour,
		},
		{
			name:     "terminating pods shorten a long requeue",
			requeue:  8 * time.Hour,
			report:   terminationReport{Terminating: 1},
			expected: constants.TerminationCheckInterval,
		},
		{
			// The reconcile that performs the downscale counts pods before the
			// workload controllers have deleted any, so the count is zero and the
			// schedule would otherwise sleep until the next transition. Nothing
			// would ever look again.
			name:           "a fresh downscale shortens the requeue even at zero",
			requeue:        8 * time.Hour,
			report:         terminationReport{},
			justScaledDown: true,
			expected:       constants.TerminationCheckInterval,
		},
		{
			// A batched scale-up already comes back sooner. Do not delay it.
			name:     "a shorter requeue is kept",
			requeue:  30 * time.Second,
			report:   terminationReport{Terminating: 3, Stuck: 3},
			expected: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyTerminationRequeue(tt.requeue, tt.report, tt.justScaledDown)
			if got != tt.expected {
				t.Errorf("requeue = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestReconcile_ReportsStuckTerminatingPod drives the real reconcile loop with a pod
// that is past its grace period, and checks that both the status field and the
// requeue come out right. The e2e test found that the count never appeared, and the
// controller is the only place that could explain it.
func TestReconcile_ReportsStuckTerminatingPod(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, lightsoutv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}

	now := time.Date(2026, 3, 21, 19, 0, 0, 0, time.UTC)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "dev"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
	// Deleted half an hour ago with a zero grace period: well past the slack.
	pod := terminatingPod("web-abc", "dev", now, 30*time.Minute, 0)

	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 5 * * *",
				Downscale: "0 17 * * *",
			},
			Namespaces: []string{"dev"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, deploy, pod, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutScheduleReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		TimeFunc: func() time.Time { return now },
	}
	key := client.ObjectKey{Name: "sched"}

	// The first reconcile only adds the finalizer.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	var got lightsoutv1alpha1.LightsOutSchedule
	if err := fakeClient.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("reading the schedule back: %v", err)
	}
	if got.Status.State != lightsoutv1alpha1.ScheduleStateDown {
		t.Fatalf("state = %q, want Down", got.Status.State)
	}
	if got.Status.StuckTerminatingPods != 1 {
		t.Errorf("status.stuckTerminatingPods = %d, want 1", got.Status.StuckTerminatingPods)
	}
	if result.RequeueAfter > constants.TerminationCheckInterval {
		t.Errorf("requeueAfter = %v, want no more than %v", result.RequeueAfter, constants.TerminationCheckInterval)
	}
}

// TestReconcile_FindsPodStuckAfterTheDownscale follows the sequence the e2e test
// drives: the downscale reconcile runs before any pod is terminating, and the pod
// only becomes overdue later. The controller has to come back on its own and find
// it. The first e2e run failed here because the reconcile slept until the next
// transition instead.
func TestReconcile_FindsPodStuckAfterTheDownscale(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, lightsoutv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}

	// The fake client stamps a real deletion timestamp when the pod is deleted, so
	// the simulated clock has to sit on the same timeline. Base everything on now
	// and build cron expressions that put this instant inside a downscale window.
	downscaleAt := time.Now().UTC()
	now := downscaleAt
	downscaleStart := downscaleAt.Add(-time.Hour)
	upscaleStart := downscaleAt.Add(6 * time.Hour)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "dev"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
	// Still running. The workload controllers have not deleted it yet, which is
	// exactly what the downscale reconcile sees.
	pod := terminatingPod("web-abc", "dev", now, 0, 0)

	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   fmt.Sprintf("%d %d * * *", upscaleStart.Minute(), upscaleStart.Hour()),
				Downscale: fmt.Sprintf("%d %d * * *", downscaleStart.Minute(), downscaleStart.Hour()),
			},
			Namespaces: []string{"dev"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, deploy, pod, schedule).
		WithStatusSubresource(schedule).
		Build()

	r := &LightsOutScheduleReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		TimeFunc: func() time.Time { return now },
	}
	key := client.ObjectKey{Name: "sched"}
	request := ctrl.Request{NamespacedName: key}

	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("finalizer reconcile failed: %v", err)
	}
	result, err := r.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("downscale reconcile failed: %v", err)
	}

	// Nothing is terminating yet, so the count is zero. The requeue must still be
	// short, or nothing ever looks again.
	var afterDownscale lightsoutv1alpha1.LightsOutSchedule
	if err := fakeClient.Get(context.Background(), key, &afterDownscale); err != nil {
		t.Fatalf("reading the schedule back: %v", err)
	}
	if afterDownscale.Status.StuckTerminatingPods != 0 {
		t.Errorf("status.stuckTerminatingPods = %d right after downscale, want 0",
			afterDownscale.Status.StuckTerminatingPods)
	}
	if result.RequeueAfter > constants.TerminationCheckInterval {
		t.Fatalf("requeueAfter = %v after downscale, want no more than %v",
			result.RequeueAfter, constants.TerminationCheckInterval)
	}

	// Delete the pod the way a workload controller would. Its finalizer keeps the
	// object, which is what a kubelet that cannot kill the container looks like.
	var stuck corev1.Pod
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "dev", Name: "web-abc"}, &stuck); err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	if err := fakeClient.Delete(context.Background(), &stuck, client.GracePeriodSeconds(0)); err != nil {
		t.Fatalf("deleting the pod: %v", err)
	}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "dev", Name: "web-abc"}, &stuck); err != nil {
		t.Fatalf("the finalizer should have kept the pod: %v", err)
	}
	if stuck.DeletionTimestamp == nil {
		t.Fatal("the pod has no deletion timestamp, so the test would prove nothing")
	}

	// The controller comes back after its own requeue and finds the pod overdue.
	now = downscaleAt.Add(constants.TerminationCheckInterval)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("follow-up reconcile failed: %v", err)
	}

	var afterPoll lightsoutv1alpha1.LightsOutSchedule
	if err := fakeClient.Get(context.Background(), key, &afterPoll); err != nil {
		t.Fatalf("reading the schedule back: %v", err)
	}
	if afterPoll.Status.StuckTerminatingPods != 1 {
		t.Errorf("status.stuckTerminatingPods = %d after the follow-up reconcile, want 1",
			afterPoll.Status.StuckTerminatingPods)
	}
}
