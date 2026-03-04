package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// recordWorkloadEvent emits a Kubernetes event on the given workload object.
// scheduleKind is the human-readable schedule type used in the message
// ("schedule" for global, "namespace schedule" for NS).
// It is a no-op when recorder is nil, result is nil, result is skipped, or the
// workload's typed pointer field is nil.
func recordWorkloadEvent(recorder events.EventRecorder, w Workload, scheduleName, scheduleKind string, scaleUp bool, result *ScaleResult) {
	if recorder == nil || result == nil || result.Skipped {
		return
	}

	var reason, action, message string
	var obj runtime.Object

	switch w.Type {
	case WorkloadTypeDeployment:
		if w.Deployment == nil {
			return
		}
		obj = w.Deployment
		if scaleUp {
			reason, action = constants.EventReasonScaledUp, constants.EventActionScaleUp
			message = fmt.Sprintf("Scaled up by LightsOut %s %q: replicas %s → %s", scheduleKind, scheduleName, result.PreviousValue, result.NewValue)
		} else {
			reason, action = constants.EventReasonScaledDown, constants.EventActionScaleDown
			message = fmt.Sprintf("Scaled down by LightsOut %s %q: replicas %s → %s", scheduleKind, scheduleName, result.PreviousValue, result.NewValue)
		}
	case WorkloadTypeStatefulSet:
		if w.StatefulSet == nil {
			return
		}
		obj = w.StatefulSet
		if scaleUp {
			reason, action = constants.EventReasonScaledUp, constants.EventActionScaleUp
			message = fmt.Sprintf("Scaled up by LightsOut %s %q: replicas %s → %s", scheduleKind, scheduleName, result.PreviousValue, result.NewValue)
		} else {
			reason, action = constants.EventReasonScaledDown, constants.EventActionScaleDown
			message = fmt.Sprintf("Scaled down by LightsOut %s %q: replicas %s → %s", scheduleKind, scheduleName, result.PreviousValue, result.NewValue)
		}
	case WorkloadTypeCronJob:
		if w.CronJob == nil {
			return
		}
		obj = w.CronJob
		if scaleUp {
			reason, action = "Resumed", "Resume"
			message = fmt.Sprintf("Resumed by LightsOut %s %q", scheduleKind, scheduleName)
		} else {
			reason, action = "Suspended", "Suspend"
			message = fmt.Sprintf("Suspended by LightsOut %s %q", scheduleKind, scheduleName)
		}
	default:
		return
	}

	recorder.Eventf(obj, nil, corev1.EventTypeNormal, reason, action, "%s", message)
}

// recordScalingEvents emits schedule-level Kubernetes events summarising a completed
// scaling pass. It is a no-op when recorder is nil.
func recordScalingEvents(recorder events.EventRecorder, scheduleObj runtime.Object, scaleUp bool, stats lightsoutv1alpha1.WorkloadStats, namespaces []string) {
	if recorder == nil {
		return
	}
	if !scaleUp && stats.DeploymentsScaled+stats.StatefulSetsScaled+stats.CronJobsSuspended > 0 {
		recorder.Eventf(scheduleObj, nil, corev1.EventTypeNormal, constants.EventReasonScaledDown, constants.EventActionScaleDown,
			"Scaled down %d deployments, %d statefulsets, suspended %d cronjobs across %d namespaces",
			stats.DeploymentsScaled, stats.StatefulSetsScaled, stats.CronJobsSuspended, len(namespaces))
	}
	totalManaged := stats.DeploymentsManaged + stats.StatefulSetsManaged + stats.CronJobsManaged
	if scaleUp && totalManaged > 0 {
		recorder.Eventf(scheduleObj, nil, corev1.EventTypeNormal, constants.EventReasonScaledUp, constants.EventActionScaleUp,
			"Scaled up workloads across %d namespaces (managing %d deployments, %d statefulsets, %d cronjobs)",
			len(namespaces), stats.DeploymentsManaged, stats.StatefulSetsManaged, stats.CronJobsManaged)
	}
}
