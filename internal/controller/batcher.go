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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// WorkloadType identifies the type of Kubernetes workload
type WorkloadType string

const (
	WorkloadTypeDeployment  WorkloadType = "Deployment"
	WorkloadTypeStatefulSet WorkloadType = "StatefulSet"
	WorkloadTypeCronJob     WorkloadType = "CronJob"
)

// Workload represents a single workload to be scaled
type Workload struct {
	Type        WorkloadType
	Name        string
	Namespace   string
	Deployment  *appsv1.Deployment
	StatefulSet *appsv1.StatefulSet
	CronJob     *batchv1.CronJob
}

// WorkloadFromDeployment creates a Workload from a Deployment
func WorkloadFromDeployment(d *appsv1.Deployment) Workload {
	return Workload{
		Type:       WorkloadTypeDeployment,
		Name:       d.Name,
		Namespace:  d.Namespace,
		Deployment: d,
	}
}

// WorkloadFromStatefulSet creates a Workload from a StatefulSet
func WorkloadFromStatefulSet(s *appsv1.StatefulSet) Workload {
	return Workload{
		Type:        WorkloadTypeStatefulSet,
		Name:        s.Name,
		Namespace:   s.Namespace,
		StatefulSet: s,
	}
}

// WorkloadFromCronJob creates a Workload from a CronJob
func WorkloadFromCronJob(c *batchv1.CronJob) Workload {
	return Workload{
		Type:      WorkloadTypeCronJob,
		Name:      c.Name,
		Namespace: c.Namespace,
		CronJob:   c,
	}
}

// scaleWorkloadsResult contains the result of scaling all workloads
type scaleWorkloadsResult struct {
	stats          lightsoutv1alpha1.WorkloadStats
	totalProcessed int
	totalFailed    int
	totalSkipped   int
	// totalWorkloads is the full collection size, set only when the pass returned early
	// (batchLimitReached or waveGateReached).
	totalWorkloads    int
	batchLimitReached bool
	// waveGateReached is true when the pass stopped at a sync wave boundary because the
	// wave just scaled has not settled yet. More waves remain.
	waveGateReached bool
	// waveProgress is how far wave-ordered scaling got, for the caller to persist in
	// status. Nil when sync waves are off or no wave boundary was reached.
	waveProgress *lightsoutv1alpha1.WaveProgress
}

// stoppedEarly reports whether the pass returned before visiting every workload. Either
// the rate limit budget ran out, or a sync wave has not settled yet.
// Workloads behind either limit are still in their old state.
func (r *scaleWorkloadsResult) stoppedEarly() bool {
	return r.batchLimitReached || r.waveGateReached
}

// scaleWorkloadsConfig carries per-reconciler parameters into the shared scaleWorkloads function.
type scaleWorkloadsConfig struct {
	// ScheduleName is the name of the schedule CR (used for label-based lookups and events).
	ScheduleName string
	// ScheduleLabel is the Prometheus metric label. Global schedules use the name. Namespace
	// schedules use "namespace/name" to avoid collisions.
	ScheduleLabel string
	// ScheduleKind is the human-readable schedule type used in event messages
	// ("schedule" for global, "namespace schedule" for NS).
	ScheduleKind string
	// Namespaces is the set of namespaces to process.
	Namespaces []string
	// ScaleUp indicates the desired direction.
	ScaleUp bool
	// RateLimit configures optional batching. A nil value means unlimited.
	RateLimit *lightsoutv1alpha1.RateLimitConfig
	// TransferOwnership, when true, re-stamps workloads that carry a foreign managed-by
	// label so the caller's schedule takes precedence (used by namespace schedules).
	TransferOwnership bool
	// ScheduleObj is the schedule resource sync wave events are recorded against.
	ScheduleObj runtime.Object
	// WaveProgress is how far wave-ordered scaling got on the previous reconcile in this
	// same direction. Nil on the first pass of a transition.
	WaveProgress *lightsoutv1alpha1.WaveProgress
	// Now is the time sync wave timeouts are measured against. Zero means time.Now().
	Now time.Time
}

// hasControllerOwner reports whether the object is controlled by another controller.
// Workloads created by an operator from its own custom resource (a StatefulSet built
// from a database CRD, for example) carry such a reference. The owning controller
// reconciles their replica count, so lightsout must not fight it.
func hasControllerOwner(refs []metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

// countManagedWorkloads fills in the *Managed counters from a collected workload slice.
// The *Scaled counters are accumulated separately as workloads are processed, because
// collection membership says nothing about whether a workload actually reached the
// scaled-down state.
func countManagedWorkloads(workloads []Workload) lightsoutv1alpha1.WorkloadStats {
	stats := lightsoutv1alpha1.WorkloadStats{}

	for _, w := range workloads {
		switch w.Type {
		case WorkloadTypeDeployment:
			stats.DeploymentsManaged++
		case WorkloadTypeStatefulSet:
			stats.StatefulSetsManaged++
		case WorkloadTypeCronJob:
			stats.CronJobsManaged++
		}
	}

	return stats
}

// isScaledDownByLightsOut reports whether the workload currently sits in the
// scaled-down state under lightsout's control, based on the annotations left on the
// object by the scaler.
//
// This reads the object rather than the ScaleResult, deliberately. A workload
// skipped as "already scaled down" is still down. One skipped because a user parked
// it at zero replicas, or because another schedule owns it, is not ours to count.
// Reading the object also stays correct across requeues in a batched run.
func isScaledDownByLightsOut(w Workload) bool {
	switch w.Type {
	case WorkloadTypeDeployment:
		return w.Deployment.Annotations[constants.OriginalReplicasAnnotation] != ""
	case WorkloadTypeStatefulSet:
		return w.StatefulSet.Annotations[constants.OriginalReplicasAnnotation] != ""
	case WorkloadTypeCronJob:
		return w.CronJob.Annotations[constants.OriginalSuspendAnnotation] == constants.SuspendedByLightsOut
	}
	return false
}

// recordScaledWorkload increments the scaled counter matching the workload type.
func recordScaledWorkload(stats *lightsoutv1alpha1.WorkloadStats, w Workload) {
	switch w.Type {
	case WorkloadTypeDeployment:
		stats.DeploymentsScaled++
	case WorkloadTypeStatefulSet:
		stats.StatefulSetsScaled++
	case WorkloadTypeCronJob:
		stats.CronJobsSuspended++
	}
}

// collectWorkloads gathers all workloads from the given namespaces, applying
// workload-type and exclude-label filters from core.
//
// When transferOwnership is true, any workload already carrying a managed-by label
// from a different schedule has its ownership re-stamped to scheduleName before being
// included. This allows a namespace schedule to take precedence over a global schedule
// that previously claimed the workload.
func collectWorkloads(
	ctx context.Context,
	c client.Client,
	namespaces []string,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	scheduleName string,
	transferOwnership bool,
) ([]Workload, error) {
	var workloads []Workload

	for _, ns := range namespaces {
		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeDeployment) {
			deploys, err := collectNamespaceDeployments(ctx, c, ns, core, scheduleName, transferOwnership)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, deploys...)
		}

		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeStatefulSet) {
			stsList, err := collectNamespaceStatefulSets(ctx, c, ns, core, scheduleName, transferOwnership)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, stsList...)
		}

		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeCronJob) {
			cjList, err := collectNamespaceCronJobs(ctx, c, ns, core, scheduleName, transferOwnership)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, cjList...)
		}
	}

	return workloads, nil
}

func collectNamespaceDeployments(ctx context.Context, c client.Client, ns string, core *lightsoutv1alpha1.LightsOutScheduleCore, scheduleName string, transferOwnership bool) ([]Workload, error) {
	logger := log.FromContext(ctx)
	var list appsv1.DeploymentList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	workloads := make([]Workload, 0, len(list.Items))
	for i := range list.Items {
		deploy := &list.Items[i]
		excluded, err := ShouldExcludeWorkload(deploy.Labels, core.ExcludeLabels)
		if err != nil {
			logger.Error(err, "error checking exclusion", "deployment", deploy.Name)
			continue
		}
		if excluded {
			continue
		}
		if !core.IncludeOwnedWorkloads && hasControllerOwner(deploy.OwnerReferences) {
			logger.V(1).Info("skipping deployment: controlled by another controller", "deployment", deploy.Name, "namespace", ns)
			continue
		}
		if transferOwnership {
			if existingOwner := deploy.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring deployment ownership to namespace schedule", "deployment", deploy.Name, "from", existingOwner)
				// This patches rather than updates. The workload comes from a List, and
				// an owning operator may have changed its resourceVersion since. Only our
				// own metadata changes, so a merge patch is sufficient and cannot conflict.
				if updateErr := applyMergePatch(ctx, c, deploy, objectPatch{
					SetAnnotations: map[string]string{constants.ManagedByAnnotation: scheduleName},
					SetLabels:      map[string]string{constants.ManagedByLabel: scheduleName},
				}); updateErr != nil {
					logger.Error(updateErr, "failed to transfer deployment ownership, skipping", "deployment", deploy.Name)
					continue
				}
			}
		}
		workloads = append(workloads, WorkloadFromDeployment(deploy))
	}
	return workloads, nil
}

func collectNamespaceStatefulSets(ctx context.Context, c client.Client, ns string, core *lightsoutv1alpha1.LightsOutScheduleCore, scheduleName string, transferOwnership bool) ([]Workload, error) {
	logger := log.FromContext(ctx)
	var list appsv1.StatefulSetList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	workloads := make([]Workload, 0, len(list.Items))
	for i := range list.Items {
		sts := &list.Items[i]
		excluded, err := ShouldExcludeWorkload(sts.Labels, core.ExcludeLabels)
		if err != nil {
			logger.Error(err, "error checking exclusion", "statefulset", sts.Name)
			continue
		}
		if excluded {
			continue
		}
		if !core.IncludeOwnedWorkloads && hasControllerOwner(sts.OwnerReferences) {
			logger.V(1).Info("skipping statefulset: controlled by another controller", "statefulset", sts.Name, "namespace", ns)
			continue
		}
		if transferOwnership {
			if existingOwner := sts.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring statefulset ownership to namespace schedule", "statefulset", sts.Name, "from", existingOwner)
				if updateErr := applyMergePatch(ctx, c, sts, objectPatch{
					SetAnnotations: map[string]string{constants.ManagedByAnnotation: scheduleName},
					SetLabels:      map[string]string{constants.ManagedByLabel: scheduleName},
				}); updateErr != nil {
					logger.Error(updateErr, "failed to transfer statefulset ownership, skipping", "statefulset", sts.Name)
					continue
				}
			}
		}
		workloads = append(workloads, WorkloadFromStatefulSet(sts))
	}
	return workloads, nil
}

func collectNamespaceCronJobs(ctx context.Context, c client.Client, ns string, core *lightsoutv1alpha1.LightsOutScheduleCore, scheduleName string, transferOwnership bool) ([]Workload, error) {
	logger := log.FromContext(ctx)
	var list batchv1.CronJobList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	workloads := make([]Workload, 0, len(list.Items))
	for i := range list.Items {
		cj := &list.Items[i]
		excluded, err := ShouldExcludeWorkload(cj.Labels, core.ExcludeLabels)
		if err != nil {
			logger.Error(err, "error checking exclusion", "cronjob", cj.Name)
			continue
		}
		if excluded {
			continue
		}
		if !core.IncludeOwnedWorkloads && hasControllerOwner(cj.OwnerReferences) {
			logger.V(1).Info("skipping cronjob: controlled by another controller", "cronjob", cj.Name, "namespace", ns)
			continue
		}
		if transferOwnership {
			if existingOwner := cj.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring cronjob ownership to namespace schedule", "cronjob", cj.Name, "from", existingOwner)
				if updateErr := applyMergePatch(ctx, c, cj, objectPatch{
					SetAnnotations: map[string]string{constants.ManagedByAnnotation: scheduleName},
					SetLabels:      map[string]string{constants.ManagedByLabel: scheduleName},
				}); updateErr != nil {
					logger.Error(updateErr, "failed to transfer cronjob ownership, skipping", "cronjob", cj.Name)
					continue
				}
			}
		}
		workloads = append(workloads, WorkloadFromCronJob(cj))
	}
	return workloads, nil
}

// scaleWorkloads handles the complete scaling workflow using a budget-based single-pass
// approach. Instead of chunking workloads into batches and blocking between them, it
// processes workloads one by one with a budget. When the budget is exhausted it returns
// early with batchLimitReached=true so the caller can requeue and yield control back to
// the controller framework.
//
// A skipped workload, one already at the target state, consumes no budget. Re-entry
// after a requeue is therefore cheap, and the annotations make the reconciler pick up
// where it left off.
//
// With sync waves enabled the same early return serves the wave boundaries. Workloads
// are processed in wave order, and the pass stops at the first boundary whose preceding
// wave has not settled, returning waveGateReached=true. Both limits yield to a requeue, and
// which one trips first does not matter, because every pass is idempotent.
func scaleWorkloads(
	ctx context.Context,
	c client.Client,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	cfg scaleWorkloadsConfig,
	recorder events.EventRecorder,
) (*scaleWorkloadsResult, error) {
	logger := log.FromContext(ctx)

	workloads, err := collectWorkloads(ctx, c, cfg.Namespaces, core, cfg.ScheduleName, cfg.TransferOwnership)
	if err != nil {
		return nil, err
	}

	// Pre-fetch the HPA lists once per namespace. Every ScaleDeployment and
	// ScaleStatefulSet call in the loop below then reuses one in-memory snapshot,
	// rather than issuing a List per workload. That is O(1) API calls per
	// namespace instead of O(workloads).
	hpaLists := make(map[string]*unstructured.UnstructuredList, len(cfg.Namespaces))
	for _, ns := range cfg.Namespaces {
		list, hpaErr := listHPAs(ctx, c, ns)
		if hpaErr != nil {
			logger.Error(hpaErr, "failed to list HPAs, HPA integration disabled for namespace", "namespace", ns)
			// nil entry - PatchHPAForDownscale/RestoreHPA gracefully no-op for this namespace
		} else {
			hpaLists[ns] = list
		}
	}

	// Determine budget: unlimited (-1) if no rate limit, otherwise batchSize
	budget := -1
	if cfg.RateLimit != nil && cfg.RateLimit.BatchSize != nil && *cfg.RateLimit.BatchSize > 0 {
		budget = *cfg.RateLimit.BatchSize
	}

	direction := "down"
	if cfg.ScaleUp {
		direction = "up"
	}

	// Sync wave state. gateWave is the wave currently being processed, and gateMembers
	// are the workloads of it the next boundary waits for.
	wavesEnabled, waveTimeout := syncWaveSettings(core)
	waveNow := cfg.Now
	if waveNow.IsZero() {
		waveNow = time.Now()
	}
	var waveProgress *lightsoutv1alpha1.WaveProgress
	gateWave := 0
	var gateMembers []Workload
	if wavesEnabled {
		// The previous wait is carried only while waves are on. Turn the field off and
		// the last wave reached would be rewritten into status forever, reporting a
		// wait that nothing waits for.
		waveProgress = cfg.WaveProgress
		sortWorkloadsByWave(workloads, cfg.ScaleUp)
		if len(workloads) > 0 {
			gateWave = syncWave(workloads[0])
		}
	}

	var totalProcessed, totalFailed, totalSkipped int
	startTime := time.Now()

	// Managed counts come from the collection. Scaled counts accumulate below from the
	// state each workload ends up in, so skipped and failed workloads are never
	// reported as scaled.
	stats := countManagedWorkloads(workloads)

	for i, w := range workloads {
		// Check for context cancellation between workloads for faster shutdown response
		select {
		case <-ctx.Done():
			logger.Info("context cancelled during workload processing, will resume on next reconcile",
				"processed", totalProcessed, "total", len(workloads))
			return &scaleWorkloadsResult{
				stats:          stats,
				totalProcessed: totalProcessed,
				totalFailed:    totalFailed,
				totalSkipped:   totalSkipped,
			}, ctx.Err()
		default:
		}

		// Wave boundary: everything up to here belongs to gateWave, and the wave after it
		// must not start until gateWave has settled.
		if wavesEnabled {
			if wave := syncWave(w); wave != gateWave {
				var hold bool
				waveProgress, hold = waveGate(ctx, recorder, cfg.ScheduleObj, waveProgress,
					gateWave, gateMembers, cfg.ScaleUp, waveTimeout, waveNow)
				if hold {
					ScalingDurationSeconds.WithLabelValues(cfg.ScheduleLabel, direction).Observe(time.Since(startTime).Seconds())
					if totalProcessed > 0 {
						ScalingBatchesTotal.WithLabelValues(cfg.ScheduleLabel, direction).Inc()
					}
					return &scaleWorkloadsResult{
						stats:           stats,
						totalProcessed:  totalProcessed,
						totalFailed:     totalFailed,
						totalSkipped:    totalSkipped,
						totalWorkloads:  len(workloads),
						waveGateReached: true,
						waveProgress:    waveProgress,
					}, nil
				}
				gateWave = wave
				gateMembers = gateMembers[:0]
			}
		}

		var scaleResult *ScaleResult
		var scaleErr error

		switch w.Type {
		case WorkloadTypeDeployment:
			scaleResult, scaleErr = ScaleDeployment(ctx, c, w.Deployment, cfg.ScheduleName, cfg.ScaleUp, hpaLists[w.Namespace])
		case WorkloadTypeStatefulSet:
			scaleResult, scaleErr = ScaleStatefulSet(ctx, c, w.StatefulSet, cfg.ScheduleName, cfg.ScaleUp, hpaLists[w.Namespace])
		case WorkloadTypeCronJob:
			scaleResult, scaleErr = ScaleCronJob(ctx, c, w.CronJob, cfg.ScheduleName, cfg.ScaleUp)
		}

		if scaleErr != nil {
			logger.Error(scaleErr, "failed to scale workload", "type", w.Type, "name", w.Name, "namespace", w.Namespace)
			ScalingErrorsTotal.WithLabelValues(cfg.ScheduleLabel, w.Namespace, string(w.Type)).Inc()
			ScalingWorkloadsProcessed.WithLabelValues(cfg.ScheduleLabel, direction, "failure").Inc()
			totalFailed++
			continue
		}

		// A workload that was scaled, or that is already in the target state under this
		// schedule, is what the next wave boundary waits for. One that failed is not: it
		// was never asked to move.
		if wavesEnabled && waveGates(w, scaleResult) {
			gateMembers = append(gateMembers, w)
		}

		// Record the resulting state before branching on Skipped. A workload skipped
		// as "already scaled down" is still down and must be counted. One skipped
		// because a user parked it at zero, or another schedule owns it, must not be.
		if isScaledDownByLightsOut(w) {
			recordScaledWorkload(&stats, w)
		}

		if scaleResult.Skipped {
			totalSkipped++
			continue
		}

		// Actual scale operation performed — consume budget
		operation := "downscale"
		if cfg.ScaleUp {
			operation = "upscale"
		}
		ScalingOperationsTotal.WithLabelValues(cfg.ScheduleLabel, w.Namespace, string(w.Type), operation).Inc()
		ScalingWorkloadsProcessed.WithLabelValues(cfg.ScheduleLabel, direction, "success").Inc()
		totalProcessed++
		recordWorkloadEvent(recorder, w, cfg.ScheduleName, cfg.ScheduleKind, cfg.ScaleUp, scaleResult)

		if budget > 0 {
			budget--
			if budget == 0 {
				// Budget exhausted — check if more workloads remain
				moreRemain := i < len(workloads)-1
				if moreRemain {
					ScalingBatchesTotal.WithLabelValues(cfg.ScheduleLabel, direction).Inc()
					ScalingDurationSeconds.WithLabelValues(cfg.ScheduleLabel, direction).Observe(time.Since(startTime).Seconds())

					return &scaleWorkloadsResult{
						stats:             stats,
						totalProcessed:    totalProcessed,
						totalFailed:       totalFailed,
						totalSkipped:      totalSkipped,
						totalWorkloads:    len(workloads),
						batchLimitReached: true,
						waveProgress:      waveProgress,
					}, nil
				}
			}
		}
	}

	// All workloads processed - record metrics
	ScalingDurationSeconds.WithLabelValues(cfg.ScheduleLabel, direction).Observe(time.Since(startTime).Seconds())
	if totalProcessed > 0 {
		ScalingBatchesTotal.WithLabelValues(cfg.ScheduleLabel, direction).Inc()
	}

	// waveProgress is carried out of a completed pass too. It records which waves scaling
	// has already moved past, so the next reconcile in this direction does not wait on
	// them again.
	return &scaleWorkloadsResult{
		stats:          stats,
		totalProcessed: totalProcessed,
		totalFailed:    totalFailed,
		totalSkipped:   totalSkipped,
		waveProgress:   waveProgress,
	}, nil
}
