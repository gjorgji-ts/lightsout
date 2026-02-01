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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// LightsOutScheduleReconciler reconciles a LightsOutSchedule object
type LightsOutScheduleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// TimeFunc returns the current time. If nil, time.Now() is used.
	// This is primarily used for testing to inject a fixed time.
	TimeFunc func() time.Time
}

// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutschedules/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *LightsOutScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the schedule
	var schedule lightsoutv1alpha1.LightsOutSchedule
	if err := r.Get(ctx, req.NamespacedName, &schedule); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("schedule not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Enrich logger with schedule context for all subsequent log calls
	logger = logger.WithValues("schedule", schedule.Name, "generation", schedule.Generation)
	ctx = log.IntoContext(ctx, logger)

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&schedule, constants.FinalizerName) {
		controllerutil.AddFinalizer(&schedule, constants.FinalizerName)
		if err := r.Update(ctx, &schedule); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Handle deletion
	if !schedule.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &schedule)
	}

	// Skip if suspended
	if schedule.Spec.Suspend {
		logger.Info("schedule is suspended, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Calculate current period
	now := time.Now()
	if r.TimeFunc != nil {
		now = r.TimeFunc()
	}
	timezone := schedule.Spec.Timezone
	if timezone == "" {
		timezone = "UTC"
	}

	period, err := CalculatePeriod(schedule.Spec.Upscale, schedule.Spec.Downscale, timezone, now)
	if err != nil {
		logger.Error(err, "failed to calculate period")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	// Add state to logger context now that we know the period
	logger = logger.WithValues("state", period.State)
	ctx = log.IntoContext(ctx, logger)

	// Discover target namespaces
	namespaces, err := DiscoverNamespaces(ctx, r.Client, &schedule.Spec)
	if err != nil {
		logger.Error(err, "failed to discover namespaces")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	logger.Info("reconciling",
		"namespaces", len(namespaces),
		"nextUpscale", period.NextUpscale,
		"nextDownscale", period.NextDownscale)

	// Process workloads
	scaleUp := period.State == "Up"

	// Scale all workloads (handles collection, batching, and metrics)
	scaleResult, err := r.scaleWorkloads(ctx, &schedule, namespaces, scaleUp)
	if err != nil {
		logger.Error(err, "failed to scale workloads")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	stats := scaleResult.stats

	// Update status
	schedule.Status.State = lightsoutv1alpha1.ScheduleState(period.State)
	schedule.Status.Namespaces = namespaces
	schedule.Status.WorkloadStats = stats
	schedule.Status.ObservedGeneration = schedule.Generation
	schedule.Status.NextUpscaleTime = &metav1.Time{Time: period.NextUpscale}
	schedule.Status.NextDownscaleTime = &metav1.Time{Time: period.NextDownscale}

	// Set Ready condition
	meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            "Successfully reconciled schedule",
		ObservedGeneration: schedule.Generation,
	})

	if err := r.Status().Update(ctx, &schedule); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// Record events for scaling operations
	if r.Recorder != nil {
		if !scaleUp && stats.DeploymentsScaled+stats.StatefulSetsScaled+stats.CronJobsSuspended > 0 {
			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "ScaledDown", "ScaleDown",
				"Scaled down %d deployments, %d statefulsets, suspended %d cronjobs across %d namespaces",
				stats.DeploymentsScaled, stats.StatefulSetsScaled, stats.CronJobsSuspended, len(namespaces))
		}

		totalManaged := stats.DeploymentsManaged + stats.StatefulSetsManaged + stats.CronJobsManaged
		if scaleUp && totalManaged > 0 {
			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "ScaledUp", "ScaleUp",
				"Scaled up workloads across %d namespaces (managing %d deployments, %d statefulsets, %d cronjobs)",
				len(namespaces), stats.DeploymentsManaged, stats.StatefulSetsManaged, stats.CronJobsManaged)
		}
	}

	// Record metrics
	stateValue := float64(0)
	if schedule.Status.State == lightsoutv1alpha1.ScheduleStateUp {
		stateValue = 1
	}
	ScheduleState.WithLabelValues(schedule.Name).Set(stateValue)

	NextTransitionSeconds.WithLabelValues(schedule.Name, "upscale").Set(time.Until(period.NextUpscale).Seconds())
	NextTransitionSeconds.WithLabelValues(schedule.Name, "downscale").Set(time.Until(period.NextDownscale).Seconds())

	ManagedWorkloads.WithLabelValues(schedule.Name, "deployment").Set(float64(stats.DeploymentsManaged))
	ManagedWorkloads.WithLabelValues(schedule.Name, "statefulset").Set(float64(stats.StatefulSetsManaged))
	ManagedWorkloads.WithLabelValues(schedule.Name, "cronjob").Set(float64(stats.CronJobsManaged))

	LastReconcileTime.WithLabelValues(schedule.Name).SetToCurrentTime()

	// Calculate requeue time (next transition)
	var requeueAfter time.Duration
	if scaleUp {
		requeueAfter = time.Until(period.NextDownscale)
	} else {
		requeueAfter = time.Until(period.NextUpscale)
	}

	// Ensure minimum requeue time of 1 minute
	if requeueAfter < time.Minute {
		requeueAfter = time.Minute
	}

	logger.Info("reconciliation complete", "requeueAfter", requeueAfter)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// BatchResult contains the result of processing a batch of workloads
type BatchResult struct {
	Processed int
	Failed    int
	Skipped   int
}

// scaleWorkloadsResult contains the result of scaling all workloads
type scaleWorkloadsResult struct {
	stats          lightsoutv1alpha1.WorkloadStats
	totalProcessed int
	totalFailed    int
	totalSkipped   int
}

// scaleWorkloads handles the complete scaling workflow including:
// - Collecting workloads from namespaces
// - Batching and processing workloads
// - Recording metrics
// - Building stats
func (r *LightsOutScheduleReconciler) scaleWorkloads(
	ctx context.Context,
	schedule *lightsoutv1alpha1.LightsOutSchedule,
	namespaces []string,
	scaleUp bool,
) (*scaleWorkloadsResult, error) {
	logger := log.FromContext(ctx)

	// Collect all workloads
	workloads, err := r.collectWorkloads(ctx, namespaces, &schedule.Spec)
	if err != nil {
		return nil, fmt.Errorf("failed to collect workloads: %w", err)
	}

	// Get rate limit config based on direction
	var rateLimit *lightsoutv1alpha1.RateLimitConfig
	if scaleUp {
		rateLimit = schedule.Spec.UpscaleRateLimit
	} else {
		rateLimit = schedule.Spec.DownscaleRateLimit
	}

	// Process workloads in batches
	startTime := time.Now()
	batches := ChunkWorkloads(workloads, rateLimit)

	direction := "down"
	if scaleUp {
		direction = "up"
	}

	// Initialize scaling progress
	if len(batches) > 0 && rateLimit != nil && rateLimit.BatchSize != nil {
		schedule.Status.ScalingProgress = &lightsoutv1alpha1.ScalingProgress{
			Total:      len(workloads),
			InProgress: true,
		}
	}

	var totalProcessed, totalFailed, totalSkipped int
	for i, batch := range batches {
		// Check for context cancellation between batches to enable graceful shutdown.
		// Note: Partial scaling is safe because the system is idempotent - already-scaled
		// workloads are skipped on the next reconciliation via annotation checks.
		select {
		case <-ctx.Done():
			logger.Info("context cancelled during batch processing, will resume on next reconcile",
				"processedBatches", i, "totalBatches", len(batches))
			return &scaleWorkloadsResult{
				stats:          r.buildStatsFromWorkloads(workloads, scaleUp),
				totalProcessed: totalProcessed,
				totalFailed:    totalFailed,
				totalSkipped:   totalSkipped,
			}, ctx.Err()
		default:
		}

		batchResult := r.processBatch(ctx, batch, schedule.Name, scaleUp)
		totalProcessed += batchResult.Processed
		totalFailed += batchResult.Failed
		totalSkipped += batchResult.Skipped

		ScalingBatchesTotal.WithLabelValues(schedule.Name, direction).Inc()

		// Update progress
		if schedule.Status.ScalingProgress != nil {
			schedule.Status.ScalingProgress.Completed = totalProcessed + totalSkipped
			schedule.Status.ScalingProgress.Failed = totalFailed
		}

		// Delay between batches (except after last batch)
		if i < len(batches)-1 && rateLimit != nil && rateLimit.DelayBetweenBatches != nil {
			select {
			case <-ctx.Done():
				logger.Info("context cancelled during batch delay, will resume on next reconcile")
				return &scaleWorkloadsResult{
					stats:          r.buildStatsFromWorkloads(workloads, scaleUp),
					totalProcessed: totalProcessed,
					totalFailed:    totalFailed,
					totalSkipped:   totalSkipped,
				}, ctx.Err()
			case <-time.After(rateLimit.DelayBetweenBatches.Duration):
			}
		}
	}

	// Record scaling duration
	ScalingDurationSeconds.WithLabelValues(schedule.Name, direction).Observe(time.Since(startTime).Seconds())

	// Clear scaling progress
	schedule.Status.ScalingProgress = nil

	// Build stats from processed workloads
	stats := r.buildStatsFromWorkloads(workloads, scaleUp)

	return &scaleWorkloadsResult{
		stats:          stats,
		totalProcessed: totalProcessed,
		totalFailed:    totalFailed,
		totalSkipped:   totalSkipped,
	}, nil
}

// processBatch scales a batch of workloads and returns the result
func (r *LightsOutScheduleReconciler) processBatch(ctx context.Context, batch []Workload, scheduleName string, scaleUp bool) BatchResult {
	logger := log.FromContext(ctx)
	result := BatchResult{}

	direction := "down"
	if scaleUp {
		direction = "up"
	}

	for _, w := range batch {
		// Check for context cancellation between workloads for faster shutdown response
		select {
		case <-ctx.Done():
			logger.V(1).Info("context cancelled during workload processing")
			return result
		default:
		}

		var scaleResult *ScaleResult
		var err error

		switch w.Type {
		case WorkloadTypeDeployment:
			scaleResult, err = ScaleDeployment(ctx, r.Client, w.Deployment, scheduleName, scaleUp)
		case WorkloadTypeStatefulSet:
			scaleResult, err = ScaleStatefulSet(ctx, r.Client, w.StatefulSet, scheduleName, scaleUp)
		case WorkloadTypeCronJob:
			scaleResult, err = ScaleCronJob(ctx, r.Client, w.CronJob, scheduleName, scaleUp)
		}

		if err != nil {
			logger.Error(err, "failed to scale workload", "type", w.Type, "name", w.Name, "namespace", w.Namespace)
			ScalingErrorsTotal.WithLabelValues(scheduleName, w.Namespace, string(w.Type)).Inc()
			ScalingWorkloadsProcessed.WithLabelValues(scheduleName, direction, "failure").Inc()
			result.Failed++
			continue
		}

		if scaleResult.Skipped {
			result.Skipped++
		} else {
			operation := "downscale"
			if scaleUp {
				operation = "upscale"
			}
			ScalingOperationsTotal.WithLabelValues(scheduleName, w.Namespace, string(w.Type), operation).Inc()
			ScalingWorkloadsProcessed.WithLabelValues(scheduleName, direction, "success").Inc()
			result.Processed++
		}
	}

	return result
}

// collectWorkloads gathers all workloads from the given namespaces
func (r *LightsOutScheduleReconciler) collectWorkloads(ctx context.Context, namespaces []string, spec *lightsoutv1alpha1.LightsOutScheduleSpec) ([]Workload, error) {
	var workloads []Workload
	logger := log.FromContext(ctx)

	for _, ns := range namespaces {
		// Collect Deployments
		if shouldProcessWorkloadType(spec.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeDeployment) {
			var deployments appsv1.DeploymentList
			if err := r.List(ctx, &deployments, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range deployments.Items {
				deploy := &deployments.Items[i]
				excluded, err := ShouldExcludeWorkload(deploy.Labels, spec.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "deployment", deploy.Name)
					continue
				}
				if excluded {
					continue
				}
				workloads = append(workloads, WorkloadFromDeployment(deploy))
			}
		}

		// Collect StatefulSets
		if shouldProcessWorkloadType(spec.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeStatefulSet) {
			var statefulsets appsv1.StatefulSetList
			if err := r.List(ctx, &statefulsets, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range statefulsets.Items {
				sts := &statefulsets.Items[i]
				excluded, err := ShouldExcludeWorkload(sts.Labels, spec.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "statefulset", sts.Name)
					continue
				}
				if excluded {
					continue
				}
				workloads = append(workloads, WorkloadFromStatefulSet(sts))
			}
		}

		// Collect CronJobs
		if shouldProcessWorkloadType(spec.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeCronJob) {
			var cronjobs batchv1.CronJobList
			if err := r.List(ctx, &cronjobs, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range cronjobs.Items {
				cj := &cronjobs.Items[i]
				excluded, err := ShouldExcludeWorkload(cj.Labels, spec.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "cronjob", cj.Name)
					continue
				}
				if excluded {
					continue
				}
				workloads = append(workloads, WorkloadFromCronJob(cj))
			}
		}
	}

	return workloads, nil
}

func (r *LightsOutScheduleReconciler) setErrorCondition(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule, err error) {
	meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "ReconcileFailed",
		Message:            err.Error(),
		ObservedGeneration: schedule.Generation,
	})
	if updateErr := r.Status().Update(ctx, schedule); updateErr != nil {
		log.FromContext(ctx).Error(updateErr, "failed to update error status")
	}
}

// buildStatsFromWorkloads creates WorkloadStats from processed workloads
func (r *LightsOutScheduleReconciler) buildStatsFromWorkloads(workloads []Workload, scaleUp bool) lightsoutv1alpha1.WorkloadStats {
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

	// For downscale, track how many were actually scaled
	if !scaleUp {
		stats.DeploymentsScaled = stats.DeploymentsManaged
		stats.StatefulSetsScaled = stats.StatefulSetsManaged
		stats.CronJobsSuspended = stats.CronJobsManaged
	}

	return stats
}

func shouldProcessWorkloadType(types []lightsoutv1alpha1.WorkloadType, target lightsoutv1alpha1.WorkloadType) bool {
	if len(types) == 0 {
		return true // Process all types if none specified
	}
	for _, t := range types {
		if t == target {
			return true
		}
	}
	return false
}

// handleDeletion restores all managed workloads to their original state before allowing deletion
func (r *LightsOutScheduleReconciler) handleDeletion(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling deletion, restoring managed workloads")

	var restoreErrors []string

	// Discover all namespaces this schedule manages
	namespaces, err := DiscoverNamespaces(ctx, r.Client, &schedule.Spec)
	if err != nil {
		logger.Error(err, "failed to discover namespaces during cleanup")
		// Continue with cleanup even if namespace discovery fails
	}

	// Restore workloads in each namespace
	for _, ns := range namespaces {
		// Restore Deployments
		deployments, err := r.listManagedDeployments(ctx, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list deployments in %s: %v", ns, err))
		} else {
			for i := range deployments {
				if _, err := ScaleDeployment(ctx, r.Client, &deployments[i], schedule.Name, true); err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("deployment %s/%s: %v", ns, deployments[i].Name, err))
				}
			}
		}

		// Restore StatefulSets
		statefulsets, err := r.listManagedStatefulSets(ctx, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list statefulsets in %s: %v", ns, err))
		} else {
			for i := range statefulsets {
				if _, err := ScaleStatefulSet(ctx, r.Client, &statefulsets[i], schedule.Name, true); err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("statefulset %s/%s: %v", ns, statefulsets[i].Name, err))
				}
			}
		}

		// Restore CronJobs
		cronjobs, err := r.listManagedCronJobs(ctx, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list cronjobs in %s: %v", ns, err))
		} else {
			for i := range cronjobs {
				if _, err := ScaleCronJob(ctx, r.Client, &cronjobs[i], schedule.Name, true); err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("cronjob %s/%s: %v", ns, cronjobs[i].Name, err))
				}
			}
		}
	}

	// Record events based on cleanup result
	if len(restoreErrors) > 0 {
		logger.Error(nil, "failed to restore some workloads during cleanup",
			"schedule", schedule.Name,
			"errors", restoreErrors)

		if r.Recorder != nil {
			r.Recorder.Eventf(schedule, nil, corev1.EventTypeWarning, "CleanupPartialFailure", "Cleanup",
				"Failed to restore %d workload(s) during deletion: %s",
				len(restoreErrors), strings.Join(restoreErrors, "; "))
		}

		// Don't remove finalizer if there were errors - this will trigger a retry
		// The controller will be requeued and attempt cleanup again
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("cleanup complete, all managed workloads restored")
	if r.Recorder != nil {
		r.Recorder.Eventf(schedule, nil, corev1.EventTypeNormal, "CleanupComplete", "Cleanup",
			"All managed workloads restored to original state")
	}

	// Remove finalizer to allow deletion
	controllerutil.RemoveFinalizer(schedule, constants.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, schedule)
}

// listManagedDeployments returns deployments managed by the given schedule
func (r *LightsOutScheduleReconciler) listManagedDeployments(ctx context.Context, namespace, scheduleName string) ([]appsv1.Deployment, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return deployments.Items, nil
}

// listManagedStatefulSets returns statefulsets managed by the given schedule
func (r *LightsOutScheduleReconciler) listManagedStatefulSets(ctx context.Context, namespace, scheduleName string) ([]appsv1.StatefulSet, error) {
	var statefulsets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulsets,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return statefulsets.Items, nil
}

// listManagedCronJobs returns cronjobs managed by the given schedule
func (r *LightsOutScheduleReconciler) listManagedCronJobs(ctx context.Context, namespace, scheduleName string) ([]batchv1.CronJob, error) {
	var cronjobs batchv1.CronJobList
	if err := r.List(ctx, &cronjobs,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return cronjobs.Items, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightsOutScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("lightsout-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&lightsoutv1alpha1.LightsOutSchedule{}).
		Named("lightsoutschedule").
		Complete(r)
}
