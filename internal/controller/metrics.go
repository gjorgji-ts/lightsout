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

var (
	// ScheduleState tracks the current state of each schedule (1=Up, 0=Down)
	ScheduleState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_schedule_state",
			Help: "Current state of schedule (1=Up, 0=Down)",
		},
		[]string{"schedule"},
	)

	// NextTransitionSeconds tracks seconds until next state transition
	NextTransitionSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_next_transition_seconds",
			Help: "Seconds until next state transition",
		},
		[]string{"schedule", "transition_type"},
	)

	// ScalingOperationsTotal counts scaling operations performed
	ScalingOperationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_operations_total",
			Help: "Total number of scaling operations performed",
		},
		[]string{"schedule", "namespace", "workload_type", "operation"},
	)

	// ScalingErrorsTotal counts scaling errors
	ScalingErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_errors_total",
			Help: "Total number of scaling errors",
		},
		[]string{"schedule", "namespace", "workload_type"},
	)

	// ManagedWorkloads tracks the number of workloads being managed
	ManagedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_managed_workloads",
			Help: "Number of workloads being managed",
		},
		[]string{"schedule", "workload_type"},
	)

	// ScalingBatchesTotal counts batches processed during scaling
	ScalingBatchesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_batches_total",
			Help: "Total number of batches processed during scaling",
		},
		[]string{"schedule", "direction"},
	)

	// ScalingWorkloadsProcessed counts workloads processed during scaling
	ScalingWorkloadsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lightsout_scaling_workloads_processed_total",
			Help: "Total workloads processed during scaling",
		},
		[]string{"schedule", "direction", "result"},
	)

	// ScalingDurationSeconds tracks time taken to complete scaling operations
	ScalingDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "lightsout_scaling_duration_seconds",
			Help:    "Time taken to complete all scaling operations",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"schedule", "direction"},
	)

	// LastReconcileTime tracks the unix timestamp of the last successful reconciliation
	LastReconcileTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lightsout_last_reconcile_timestamp_seconds",
			Help: "Unix timestamp of last successful reconciliation",
		},
		[]string{"schedule"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		ScheduleState,
		NextTransitionSeconds,
		ScalingOperationsTotal,
		ScalingErrorsTotal,
		ManagedWorkloads,
		ScalingBatchesTotal,
		ScalingWorkloadsProcessed,
		ScalingDurationSeconds,
		LastReconcileTime,
	)
}
