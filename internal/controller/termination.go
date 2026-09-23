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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// maxReportedStuckPods caps the pod names in the warning event, so a namespace
// with hundreds of wedged pods stays readable.
const maxReportedStuckPods = 5

// terminationReport describes what is still running in the managed namespaces
// after a downscale.
type terminationReport struct {
	// Terminating counts every pod with a deletion timestamp. Any of them means
	// the namespace has not released its compute, so the controller keeps watching.
	Terminating int
	// Stuck counts the pods well past their grace period.
	Stuck int
	// StuckByNamespace includes namespaces with a zero count, so the metric can
	// be reset rather than left stale.
	StuckByNamespace map[string]int
	// StuckNames holds "namespace/name", sorted, for events and logs.
	StuckNames []string
}

// countTerminatingPods reports which pods are on their way out, and which have
// stopped making progress.
//
// Scaling to zero only writes the spec. Nothing waits for the pods, so a pod the
// kubelet cannot kill keeps its node alive while the schedule reports Down. This
// is the only place that notices.
//
// Finished pods have released their compute. Pods inside their grace period are
// shutting down normally. Neither is worth alerting on.
func countTerminatingPods(
	ctx context.Context,
	c client.Client,
	namespaces []string,
	now time.Time,
) (terminationReport, error) {
	report := terminationReport{StuckByNamespace: make(map[string]int, len(namespaces))}

	for _, ns := range namespaces {
		report.StuckByNamespace[ns] = 0

		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(ns)); err != nil {
			return report, err
		}

		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.DeletionTimestamp == nil {
				continue
			}
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			report.Terminating++

			if !isTerminationOverdue(pod, now) {
				continue
			}
			report.Stuck++
			report.StuckByNamespace[ns]++
			report.StuckNames = append(report.StuckNames, ns+"/"+pod.Name)
		}
	}

	sort.Strings(report.StuckNames)
	return report, nil
}

// isTerminationOverdue reports whether a pod outlived its own grace period by
// more than the slack. Judging against deletionGracePeriodSeconds rather than a
// fixed number gives a long preStop hook the time it asked for.
func isTerminationOverdue(pod *corev1.Pod, now time.Time) bool {
	grace := time.Duration(0)
	if pod.DeletionGracePeriodSeconds != nil {
		grace = time.Duration(*pod.DeletionGracePeriodSeconds) * time.Second
	}
	deadline := pod.DeletionTimestamp.Time.Add(grace).Add(constants.TerminationGraceSlack)
	return now.After(deadline)
}

// reportStuckTerminatingPods publishes the report to the metric, the log and,
// when pods are stuck, a warning event. The gauge is written for every managed
// namespace, zero included, so a recovered namespace stops alerting.
func reportStuckTerminatingPods(
	ctx context.Context,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	scheduleLabel string,
	report terminationReport,
) {
	for ns, count := range report.StuckByNamespace {
		StuckTerminatingPods.WithLabelValues(scheduleLabel, ns).Set(float64(count))
	}

	if report.Stuck == 0 {
		return
	}

	names := report.StuckNames
	suffix := ""
	if len(names) > maxReportedStuckPods {
		suffix = ", ..."
		names = names[:maxReportedStuckPods]
	}

	log.FromContext(ctx).Info("pods are stuck terminating after downscale",
		"count", report.Stuck, "pods", strings.Join(names, ", "))

	if recorder != nil && scheduleObj != nil {
		recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, constants.EventReasonPodsStuckTerminating, constants.EventActionScaleDown,
			"%d pod(s) are still running well past their termination grace period and keep their nodes alive: %s%s",
			report.Stuck, strings.Join(names, ", "), suffix)
	}
}

// clearStuckTerminatingPods zeroes the gauge, so a series stops alerting once the
// schedule is back up.
func clearStuckTerminatingPods(scheduleLabel string, namespaces []string) {
	for _, ns := range namespaces {
		StuckTerminatingPods.WithLabelValues(scheduleLabel, ns).Set(0)
	}
}

// applyTerminationRequeue shortens the requeue while pods may still be on their
// way out. The next reconcile is otherwise hours away, so a pod that fails to go
// away would keep its node all night before anything noticed.
//
// justScaledDown covers the downscale reconcile itself. It writes the replica
// count and counts pods in the same pass, but the workload controllers need a
// moment to delete anything, so that first count is almost always zero. Without
// it the controller sleeps before a single pod starts terminating.
func applyTerminationRequeue(requeueAfter time.Duration, report terminationReport, justScaledDown bool) time.Duration {
	if report.Terminating == 0 && !justScaledDown {
		return requeueAfter
	}
	return min(requeueAfter, constants.TerminationCheckInterval)
}
