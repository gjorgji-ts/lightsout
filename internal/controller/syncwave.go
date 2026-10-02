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
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// syncWaveSettings reports whether this schedule orders scaling by ArgoCD sync wave,
// and how long one wave is waited for before scaling moves on without it.
//
// The timeout is the ArgoCD warmup timeout. A wave wait and a warmup wait are the same
// wait on different scopes, so one knob covers both.
func syncWaveSettings(core *lightsoutv1alpha1.LightsOutScheduleCore) (bool, time.Duration) {
	if core.ArgoCD == nil || !core.ArgoCD.SyncWaves {
		return false, 0
	}
	timeout := constants.DefaultWarmupTimeout
	if core.ArgoCD.WarmupTimeout != nil {
		timeout = core.ArgoCD.WarmupTimeout.Duration
	}
	return true, timeout
}

// syncWave returns the workload's ArgoCD sync wave.
//
// A workload without the annotation is wave 0, and so is one whose value is not an
// integer. ArgoCD ignores an unparseable value rather than failing the sync, and a
// typo in an annotation must not quietly reorder a shutdown.
func syncWave(w Workload) int {
	wave, err := strconv.Atoi(strings.TrimSpace(workloadAnnotations(w)[constants.ArgoCDSyncWaveAnnotation]))
	if err != nil {
		return 0
	}
	return wave
}

// workloadAnnotations returns the annotations of whichever typed object the workload holds.
func workloadAnnotations(w Workload) map[string]string {
	switch w.Type {
	case WorkloadTypeDeployment:
		return w.Deployment.Annotations
	case WorkloadTypeStatefulSet:
		return w.StatefulSet.Annotations
	case WorkloadTypeCronJob:
		return w.CronJob.Annotations
	}
	return nil
}

// waveBefore reports whether wave a is processed before wave b. Upscale runs the waves
// in ascending order, downscale in reverse, so what a dependency comes up before it also
// goes down after.
func waveBefore(a, b int, scaleUp bool) bool {
	if scaleUp {
		return a < b
	}
	return a > b
}

// sortWorkloadsByWave puts workloads in the order the waves are processed.
//
// Namespace and name break ties so the order is identical on every pass. A wave spans
// every namespace the schedule manages. The dependency a workload waits for is often
// in another namespace, and ordering per namespace would not express that.
func sortWorkloadsByWave(workloads []Workload, scaleUp bool) {
	slices.SortStableFunc(workloads, func(a, b Workload) int {
		if wa, wb := syncWave(a), syncWave(b); wa != wb {
			if waveBefore(wa, wb, scaleUp) {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// workloadSettled reports whether a workload has reached the state this direction asks
// of it: every replica ready on upscale, every pod gone on downscale.
//
// The generation check does more work than the counts. A workload patched moments ago
// still carries the status of the replica count it had before. Without the check, a
// just-zeroed Deployment would read as settled, and the next wave would start against
// a dependency whose pods are still running.
//
// CronJobs always count as settled. Suspending one stops future jobs and says nothing
// about what is running, so waiting on it would only burn the wave timeout.
func workloadSettled(w Workload, scaleUp bool) bool {
	switch w.Type {
	case WorkloadTypeDeployment:
		d := w.Deployment
		if d.Status.ObservedGeneration < d.Generation {
			return false
		}
		if !scaleUp {
			return d.Status.Replicas == 0
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		return d.Status.ReadyReplicas >= desired
	case WorkloadTypeStatefulSet:
		s := w.StatefulSet
		if s.Status.ObservedGeneration < s.Generation {
			return false
		}
		if !scaleUp {
			return s.Status.Replicas == 0
		}
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		return s.Status.ReadyReplicas >= desired
	}
	return true
}

// waveGates reports whether the wave gate should wait for this workload.
//
// A workload another schedule owns, or one a user parked at zero, never reaches the
// state this pass asked for. The pass asked nothing of it. Waiting on those would stall
// every later wave until the timeout, on every single transition.
func waveGates(w Workload, result *ScaleResult) bool {
	if w.Type == WorkloadTypeCronJob {
		return false
	}
	if !result.Skipped {
		return true
	}
	switch result.SkipReason {
	case skipReasonDifferentSchedule, skipReasonNotManaged, skipReasonUserParked:
		return false
	}
	// Everything left is ours and already in the target state. One example is a
	// workload scaled down on an earlier pass whose pods may still be terminating.
	return true
}

// waveSettled reports whether every gated member of a wave reached its target state,
// naming the first that has not for the log line.
func waveSettled(members []Workload, scaleUp bool) (bool, string) {
	for _, w := range members {
		if !workloadSettled(w, scaleUp) {
			return false, fmt.Sprintf("%s %s/%s", w.Type, w.Namespace, w.Name)
		}
	}
	return true, ""
}

// waveGate decides whether scaling stops at a wave boundary, and carries the wait for a
// wave forward across reconciles.
//
// Progress is monotonic. Once scaling has moved past a wave, that wave is never waited
// on again in the same direction. Without that, a wave that timed out would restart its
// clock on the next reconcile, and the schedule would never reach its last wave. The
// caller drops the stored progress when the direction flips, because the wave order
// reverses with it.
//
// The returned progress is what the caller persists, whether or not it holds, so the
// next reconcile knows how far this one got.
func waveGate(
	ctx context.Context,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	prev *lightsoutv1alpha1.WaveProgress,
	wave int,
	members []Workload,
	scaleUp bool,
	timeout time.Duration,
	now time.Time,
) (*lightsoutv1alpha1.WaveProgress, bool) {
	logger := log.FromContext(ctx)

	if prev != nil && waveBefore(wave, prev.Wave, scaleUp) {
		return prev, false
	}

	since := now
	if prev != nil && prev.Wave == wave {
		since = prev.Since.Time
	}
	progress := &lightsoutv1alpha1.WaveProgress{Wave: wave, Since: metav1.Time{Time: since}}

	settled, blocker := waveSettled(members, scaleUp)
	if settled {
		return progress, false
	}

	if now.Sub(since) >= timeout {
		logger.Info("sync wave did not settle within the timeout, continuing with the next wave",
			"wave", wave, "timeout", timeout, "waitingOn", blocker)
		if recorder != nil {
			recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, constants.EventReasonSyncWaveTimeout, "SyncWave",
				"Sync wave %d did not settle within %s (waiting on %s), continuing with the next wave",
				wave, timeout, blocker)
		}
		return progress, false
	}

	logger.Info("holding at sync wave boundary", "wave", wave, "waitingOn", blocker)
	return progress, true
}
