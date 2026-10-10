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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Prometheus metric label names.
const (
	labelSchedule       = "schedule"
	labelTransitionType = "transition_type"
	labelNamespace      = "namespace"
	labelWorkloadType   = "workload_type"
	labelKind           = "kind"
	labelOperation      = "operation"
	labelDirection      = "direction"
	labelResult         = "result"
)

var (
	// ScheduleState tracks the current state of each schedule (0=Down, 1=Up, 2=WarmingUp)
	ScheduleState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_schedule_state",
			Help: "Current state of schedule (0=Down, 1=Up, 2=WarmingUp)",
		},
		[]string{labelSchedule},
	)

	// NextTransitionSeconds tracks seconds until next state transition
	NextTransitionSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_next_transition_seconds",
			Help: "Seconds until next state transition",
		},
		[]string{labelSchedule, labelTransitionType},
	)

	// ScalingOperationsTotal counts scaling operations performed
	ScalingOperationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_operations_total",
			Help: "Total number of scaling operations performed",
		},
		[]string{labelSchedule, labelNamespace, labelWorkloadType, labelOperation},
	)

	// ScalingErrorsTotal counts scaling errors
	ScalingErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_errors_total",
			Help: "Total number of scaling errors",
		},
		[]string{labelSchedule, labelNamespace, labelWorkloadType},
	)

	// ManagedWorkloads tracks the number of workloads being managed
	ManagedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_managed_workloads",
			Help: "Number of workloads being managed",
		},
		[]string{labelSchedule, labelWorkloadType},
	)

	// ScaledWorkloads tracks how many managed workloads are currently off: deployments
	// and statefulsets at zero replicas, and suspended cronjobs.
	//
	// ManagedWorkloads alone cannot answer the question the schedule exists to answer.
	// A schedule that reports 400 managed workloads has released nothing until they
	// are actually at zero. A downscale that half fails leaves the difference here.
	ScaledWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_scaled_workloads",
			Help: "Managed workloads currently scaled to zero or suspended",
		},
		[]string{labelSchedule, labelWorkloadType},
	)

	// ManagedCustomResources tracks how many operator-managed custom resources each
	// schedule matches, by kind.
	//
	// The scaling counters cannot answer this. A counter only moves at a transition,
	// and restarts with the operator pod. After a restart, nothing reports which kinds
	// a schedule manages until the next transition fires. This gauge is written on
	// every reconcile, so the series exists as soon as the schedule is reconciled.
	ManagedCustomResources = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_managed_custom_resources",
			Help: "Operator-managed custom resources matched by a schedule, by kind",
		},
		[]string{labelSchedule, labelKind},
	)

	// ScalingBatchesTotal counts batches processed during scaling
	ScalingBatchesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_batches_total",
			Help: "Total number of batches processed during scaling",
		},
		[]string{labelSchedule, labelDirection},
	)

	// ScalingWorkloadsProcessed counts workloads processed during scaling
	ScalingWorkloadsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_workloads_processed_total",
			Help: "Total workloads processed during scaling",
		},
		[]string{labelSchedule, labelDirection, labelResult},
	)

	// ScalingDurationSeconds tracks time taken to complete scaling operations
	ScalingDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "lightsout_scaling_duration_seconds",
			Help:    "Time taken to complete all scaling operations",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{labelSchedule, labelDirection},
	)

	// StuckTerminatingPods tracks pods that outlived their termination grace period
	// after a downscale. Downscale only writes the spec, so a pod the kubelet cannot
	// kill keeps its node alive while the schedule reports Down. Alert on this.
	StuckTerminatingPods = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_stuck_terminating_pods",
			Help: "Pods still running well past their termination grace period after a downscale",
		},
		[]string{labelSchedule, labelNamespace},
	)

	// LastReconcileTime tracks the unix timestamp of the last successful reconciliation
	LastReconcileTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_last_reconcile_timestamp_seconds",
			Help: "Unix timestamp of last successful reconciliation",
		},
		[]string{labelSchedule},
	)
)

// clearScheduleMetrics drops every series a schedule published.
//
// A Prometheus child lives until something deletes it. A deleted schedule would
// otherwise keep reporting its last state for as long as the operator pod runs. On a
// cluster where schedules come and go, those ghosts accumulate in the registry and in
// every dashboard that lists schedules.
func clearScheduleMetrics(scheduleLabel string) {
	match := prometheus.Labels{labelSchedule: scheduleLabel}

	ScheduleState.DeletePartialMatch(match)
	NextTransitionSeconds.DeletePartialMatch(match)
	ScalingOperationsTotal.DeletePartialMatch(match)
	ScalingErrorsTotal.DeletePartialMatch(match)
	ManagedWorkloads.DeletePartialMatch(match)
	ScaledWorkloads.DeletePartialMatch(match)
	ManagedCustomResources.DeletePartialMatch(match)
	ScalingBatchesTotal.DeletePartialMatch(match)
	ScalingWorkloadsProcessed.DeletePartialMatch(match)
	ScalingDurationSeconds.DeletePartialMatch(match)
	StuckTerminatingPods.DeletePartialMatch(match)
	LastReconcileTime.DeletePartialMatch(match)
}

func init() {
	metrics.Registry.MustRegister(
		ScheduleState,
		NextTransitionSeconds,
		ScalingOperationsTotal,
		ScalingErrorsTotal,
		ManagedWorkloads,
		ScaledWorkloads,
		ManagedCustomResources,
		ScalingBatchesTotal,
		ScalingWorkloadsProcessed,
		ScalingDurationSeconds,
		StuckTerminatingPods,
		LastReconcileTime,
	)
}
