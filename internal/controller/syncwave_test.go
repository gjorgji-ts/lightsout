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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// waved builds a Deployment carrying an ArgoCD sync wave annotation.
func waved(name, namespace, wave string, replicas int32) *appsv1.Deployment {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(replicas)},
	}
	if wave != "" {
		deploy.Annotations = map[string]string{constants.ArgoCDSyncWaveAnnotation: wave}
	}
	return deploy
}

func TestSyncWave(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{"no annotation", "", 0},
		{"zero", "0", 0},
		{"positive", "3", 3},
		{"negative", "-2", -2},
		{"padded", "  5  ", 5},
		{"not a number", "second", 0},
		{"empty value", " ", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WorkloadFromDeployment(waved("app", "ns1", tt.value, 1))
			if got := syncWave(w); got != tt.want {
				t.Errorf("syncWave() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSyncWave_StatefulSetAndCronJob(t *testing.T) {
	sts := WorkloadFromStatefulSet(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "db",
			Namespace:   "ns1",
			Annotations: map[string]string{constants.ArgoCDSyncWaveAnnotation: "-1"},
		},
	})
	if got := syncWave(sts); got != -1 {
		t.Errorf("statefulset syncWave() = %d, want -1", got)
	}

	cj := WorkloadFromCronJob(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "report",
			Namespace:   "ns1",
			Annotations: map[string]string{constants.ArgoCDSyncWaveAnnotation: "7"},
		},
	})
	if got := syncWave(cj); got != 7 {
		t.Errorf("cronjob syncWave() = %d, want 7", got)
	}
}

func TestSyncWaveSettings(t *testing.T) {
	tests := []struct {
		name        string
		core        *lightsoutv1alpha1.LightsOutScheduleCore
		wantEnabled bool
		wantTimeout time.Duration
	}{
		{
			"no argocd config",
			&lightsoutv1alpha1.LightsOutScheduleCore{},
			false, 0,
		},
		{
			"argocd without sync waves",
			&lightsoutv1alpha1.LightsOutScheduleCore{ArgoCD: &lightsoutv1alpha1.ArgoCDConfig{}},
			false, 0,
		},
		{
			"sync waves with default timeout",
			&lightsoutv1alpha1.LightsOutScheduleCore{ArgoCD: &lightsoutv1alpha1.ArgoCDConfig{SyncWaves: true}},
			true, constants.DefaultWarmupTimeout,
		},
		{
			"sync waves with explicit timeout",
			&lightsoutv1alpha1.LightsOutScheduleCore{ArgoCD: &lightsoutv1alpha1.ArgoCDConfig{
				SyncWaves:     true,
				WarmupTimeout: &metav1.Duration{Duration: 2 * time.Minute},
			}},
			true, 2 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, timeout := syncWaveSettings(tt.core)
			if enabled != tt.wantEnabled {
				t.Errorf("enabled = %v, want %v", enabled, tt.wantEnabled)
			}
			if timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", timeout, tt.wantTimeout)
			}
		})
	}
}

func TestSortWorkloadsByWave(t *testing.T) {
	// Deliberately unsorted, and with two workloads sharing wave 1 across namespaces.
	build := func() []Workload {
		return []Workload{
			WorkloadFromDeployment(waved("web", "ns2", "2", 1)),
			WorkloadFromDeployment(waved("api", "ns1", "1", 1)),
			WorkloadFromDeployment(waved("db", "ns1", "-1", 1)),
			WorkloadFromDeployment(waved("cache", "ns1", "", 1)),
			WorkloadFromDeployment(waved("queue", "ns0", "1", 1)),
		}
	}

	names := func(ws []Workload) []string {
		out := make([]string, 0, len(ws))
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return out
	}

	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	up := build()
	sortWorkloadsByWave(up, true)
	wantUp := []string{"db", "cache", "queue", "api", "web"}
	if !equal(names(up), wantUp) {
		t.Errorf("upscale order = %v, want %v", names(up), wantUp)
	}

	down := build()
	sortWorkloadsByWave(down, false)
	wantDown := []string{"web", "queue", "api", "cache", "db"}
	if !equal(names(down), wantDown) {
		t.Errorf("downscale order = %v, want %v", names(down), wantDown)
	}
}

func TestWorkloadSettled(t *testing.T) {
	deployment := func(generation, observed int64, desired, ready, current int32) Workload {
		return WorkloadFromDeployment(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1", Generation: generation},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(desired)},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: observed,
				ReadyReplicas:      ready,
				Replicas:           current,
			},
		})
	}

	tests := []struct {
		name    string
		w       Workload
		scaleUp bool
		want    bool
	}{
		{"up: all replicas ready", deployment(2, 2, 3, 3, 3), true, true},
		{"up: some replicas pending", deployment(2, 2, 3, 1, 3), true, false},
		{"up: status not yet observed", deployment(3, 2, 3, 3, 3), true, false},
		{"up: desired zero is nothing to wait for", deployment(2, 2, 0, 0, 0), true, true},
		{"down: all pods gone", deployment(3, 3, 0, 0, 0), false, true},
		{"down: pods still terminating", deployment(3, 3, 0, 0, 2), false, false},
		{"down: status not yet observed", deployment(3, 2, 0, 0, 0), false, false},
		{
			"statefulset up: ready",
			WorkloadFromStatefulSet(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns1", Generation: 1},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(2))},
				Status:     appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 2},
			}),
			true, true,
		},
		{
			"statefulset down: one pod left",
			WorkloadFromStatefulSet(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns1", Generation: 2},
				Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(0))},
				Status:     appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 1},
			}),
			false, false,
		},
		{
			"cronjob never gates",
			WorkloadFromCronJob(&batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "report", Namespace: "ns1"},
			}),
			false, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workloadSettled(tt.w, tt.scaleUp); got != tt.want {
				t.Errorf("workloadSettled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWaveGates(t *testing.T) {
	deploy := WorkloadFromDeployment(waved("app", "ns1", "1", 3))
	cronjob := WorkloadFromCronJob(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "report", Namespace: "ns1"},
	})

	tests := []struct {
		name   string
		w      Workload
		result *ScaleResult
		want   bool
	}{
		{"scaled now", deploy, &ScaleResult{}, true},
		{"already scaled down by us", deploy, &ScaleResult{Skipped: true, SkipReason: "already scaled down"}, true},
		{"owned by another schedule", deploy, &ScaleResult{Skipped: true, SkipReason: skipReasonDifferentSchedule}, false},
		{"not managed", deploy, &ScaleResult{Skipped: true, SkipReason: skipReasonNotManaged}, false},
		{"parked at zero by a user", deploy, &ScaleResult{Skipped: true, SkipReason: skipReasonUserParked}, false},
		{"cronjob", cronjob, &ScaleResult{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := waveGates(tt.w, tt.result); got != tt.want {
				t.Errorf("waveGates() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWaveGate(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	timeout := 10 * time.Minute

	unsettled := []Workload{WorkloadFromDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns1", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, ReadyReplicas: 0, Replicas: 3},
	})}
	settled := []Workload{WorkloadFromDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns1", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, ReadyReplicas: 3, Replicas: 3},
	})}

	t.Run("holds on an unsettled wave and starts the clock", func(t *testing.T) {
		progress, hold := waveGate(context.Background(), nil, nil, nil, 1, unsettled, true, timeout, now)
		if !hold {
			t.Fatal("expected the gate to hold")
		}
		if progress.Wave != 1 || !progress.Since.Time.Equal(now) {
			t.Errorf("progress = %+v, want wave 1 since %v", progress, now)
		}
	})

	t.Run("passes a settled wave", func(t *testing.T) {
		progress, hold := waveGate(context.Background(), nil, nil, nil, 1, settled, true, timeout, now)
		if hold {
			t.Fatal("expected the gate to pass")
		}
		if progress.Wave != 1 {
			t.Errorf("progress wave = %d, want 1", progress.Wave)
		}
	})

	t.Run("keeps the original start time while waiting", func(t *testing.T) {
		started := now.Add(-3 * time.Minute)
		prev := &lightsoutv1alpha1.WaveProgress{Wave: 1, Since: metav1.Time{Time: started}}
		progress, hold := waveGate(context.Background(), nil, nil, prev, 1, unsettled, true, timeout, now)
		if !hold {
			t.Fatal("expected the gate to hold: the timeout has not elapsed")
		}
		if !progress.Since.Time.Equal(started) {
			t.Errorf("since = %v, want the original %v", progress.Since.Time, started)
		}
	})

	t.Run("gives up once the timeout elapses", func(t *testing.T) {
		prev := &lightsoutv1alpha1.WaveProgress{Wave: 1, Since: metav1.Time{Time: now.Add(-timeout)}}
		_, hold := waveGate(context.Background(), nil, nil, prev, 1, unsettled, true, timeout, now)
		if hold {
			t.Error("expected the gate to pass after the timeout")
		}
	})

	// Without this a wave that timed out would restart its clock on the next reconcile
	// and the schedule would never reach the waves behind it.
	t.Run("never waits on a wave scaling has moved past", func(t *testing.T) {
		prev := &lightsoutv1alpha1.WaveProgress{Wave: 2, Since: metav1.Time{Time: now}}
		progress, hold := waveGate(context.Background(), nil, nil, prev, 1, unsettled, true, timeout, now)
		if hold {
			t.Error("expected the gate at wave 1 to be skipped on the way up")
		}
		if progress != prev {
			t.Errorf("progress = %+v, want the stored %+v", progress, prev)
		}
	})

	t.Run("past means the other direction when scaling down", func(t *testing.T) {
		prev := &lightsoutv1alpha1.WaveProgress{Wave: 1, Since: metav1.Time{Time: now}}

		if _, hold := waveGate(context.Background(), nil, nil, prev, 2, unsettled, false, timeout, now); hold {
			t.Error("wave 2 is already past on the way down, expected the gate to be skipped")
		}
		if _, hold := waveGate(context.Background(), nil, nil, prev, 0, unsettled, false, timeout, now); !hold {
			t.Error("wave 0 is still ahead on the way down, expected the gate to hold")
		}
	})
}

// waveScheme builds the scheme the wave scaling tests need.
func waveScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)
	return scheme
}

func waveSchedule(timeout time.Duration) *lightsoutv1alpha1.LightsOutSchedule {
	return &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				ArgoCD: &lightsoutv1alpha1.ArgoCDConfig{
					SyncWaves:     true,
					WarmupTimeout: &metav1.Duration{Duration: timeout},
				},
			},
		},
	}
}

// replicas returns the current replica count of a deployment in the ns1 test namespace.
func replicas(t *testing.T, c client.Client, name string) int32 {
	t.Helper()
	var deploy appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: name}, &deploy); err != nil {
		t.Fatalf("get ns1/%s: %v", name, err)
	}
	if deploy.Spec.Replicas == nil {
		return 1
	}
	return *deploy.Spec.Replicas
}

func TestScaleWorkloads_WaveGateStopsAtTheFirstBoundary(t *testing.T) {
	scheme := waveScheme()
	// Zeroing the replica count does not remove the pods, so the status still reports the
	// three that are now terminating. That is what the gate waits for.
	api := waved("api", "ns1", "1", 3)
	api.Generation = 1
	api.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 3, ReadyReplicas: 3}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(waved("db", "ns1", "-1", 1), api).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := waveSchedule(10 * time.Minute)
	now := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)

	// Downscale runs the waves in reverse, so "api" (wave 1) goes first and "db" (wave -1)
	// waits until api's pods are gone.
	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, nil, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.waveGateReached {
		t.Error("expected the pass to stop at a wave boundary")
	}
	if result.totalProcessed != 1 {
		t.Errorf("processed %d workloads, want 1 (only wave 1)", result.totalProcessed)
	}
	if result.waveProgress == nil || result.waveProgress.Wave != 1 {
		t.Fatalf("waveProgress = %+v, want wave 1", result.waveProgress)
	}
	if !result.waveProgress.Since.Time.Equal(now) {
		t.Errorf("waveProgress.Since = %v, want %v", result.waveProgress.Since.Time, now)
	}

	if got := replicas(t, fakeClient, "api"); got != 0 {
		t.Errorf("api replicas = %d, want 0", got)
	}
	if got := replicas(t, fakeClient, "db"); got != 1 {
		t.Errorf("db replicas = %d, want 1: wave -1 must wait for wave 1 to settle", got)
	}
}

func TestScaleWorkloads_WaveGateProceedsOnceSettled(t *testing.T) {
	scheme := waveScheme()
	// "api" is already down with no pods left, so the gate on its wave passes and the
	// pass continues into wave -1.
	api := waved("api", "ns1", "1", 0)
	api.Annotations[constants.ManagedByAnnotation] = "test-schedule"
	api.Annotations[constants.OriginalReplicasAnnotation] = "3"

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(waved("db", "ns1", "-1", 1), api).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := waveSchedule(10 * time.Minute)
	now := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)

	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, nil, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.waveGateReached {
		t.Error("expected the pass to run through every wave")
	}
	if got := replicas(t, fakeClient, "db"); got != 0 {
		t.Errorf("db replicas = %d, want 0: wave 1 had settled", got)
	}
}

func TestScaleWorkloads_WaveGateGivesUpAfterTheTimeout(t *testing.T) {
	scheme := waveScheme()
	down := waved("api", "ns1", "1", 0)
	down.Annotations[constants.ManagedByAnnotation] = "test-schedule"
	down.Annotations[constants.OriginalReplicasAnnotation] = "3"
	// A pod the kubelet never killed keeps the wave unsettled for good.
	down.Status = appsv1.DeploymentStatus{Replicas: 1}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(waved("db", "ns1", "-1", 1), down).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := waveSchedule(5 * time.Minute)
	now := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)

	// Still inside the timeout: the gate holds.
	prev := &lightsoutv1alpha1.WaveProgress{Wave: 1, Since: metav1.Time{Time: now.Add(-time.Minute)}}
	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, prev, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.waveGateReached {
		t.Fatal("expected the gate to hold one minute into a five minute timeout")
	}
	if got := replicas(t, fakeClient, "db"); got != 1 {
		t.Errorf("db replicas = %d, want 1 while the gate holds", got)
	}

	// Past the timeout: scaling continues without the stuck wave.
	prev = &lightsoutv1alpha1.WaveProgress{Wave: 1, Since: metav1.Time{Time: now.Add(-6 * time.Minute)}}
	result, err = r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, prev, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.waveGateReached {
		t.Error("expected the gate to give up after the timeout")
	}
	if got := replicas(t, fakeClient, "db"); got != 0 {
		t.Errorf("db replicas = %d, want 0 after the wave timed out", got)
	}
}

// A workload another schedule owns never reaches the state this pass asked for, so the
// gate must not wait for it. Without this the first wave holding a foreign workload would
// stall every transition until the timeout.
func TestScaleWorkloads_WaveGateIgnoresWorkloadsItDoesNotOwn(t *testing.T) {
	scheme := waveScheme()
	foreign := waved("api", "ns1", "1", 3)
	foreign.Annotations[constants.ManagedByAnnotation] = "somebody-else"

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(waved("db", "ns1", "-1", 1), foreign).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := waveSchedule(10 * time.Minute)
	now := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)

	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, nil, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.waveGateReached {
		t.Error("expected no hold: the only workload in wave 1 belongs to another schedule")
	}
	if got := replicas(t, fakeClient, "db"); got != 0 {
		t.Errorf("db replicas = %d, want 0", got)
	}
}

// Without sync waves the collection order and the single-pass behaviour must not change.
func TestScaleWorkloads_WavesDisabledScalesEverythingInOnePass(t *testing.T) {
	scheme := waveScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			waved("db", "ns1", "-1", 1),
			waved("api", "ns1", "1", 3),
		).
		Build()

	r := &LightsOutScheduleReconciler{Client: fakeClient, Scheme: scheme}
	schedule := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule"},
	}

	// A schedule that had sync waves on and turned them off still carries the last wave
	// it waited for. The pass must drop it rather than rewrite it into status forever.
	stale := &lightsoutv1alpha1.WaveProgress{
		Wave:  1,
		Since: metav1.Time{Time: time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)},
	}
	result, err := r.scaleWorkloads(context.Background(), schedule, []string{"ns1"}, false, nil, stale, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.waveGateReached {
		t.Error("expected no wave gate when sync waves are off")
	}
	if result.waveProgress != nil {
		t.Errorf("waveProgress = %+v, want nil when sync waves are off", result.waveProgress)
	}
	if result.totalProcessed != 2 {
		t.Errorf("processed %d workloads, want 2", result.totalProcessed)
	}
}
