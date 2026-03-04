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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// LightsOutNamespaceScheduleReconciler reconciles a LightsOutNamespaceSchedule object
type LightsOutNamespaceScheduleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// TimeFunc returns the current time. If nil, time.Now() is used.
	// This is primarily used for testing to inject a fixed time.
	TimeFunc func() time.Time
}

// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutnamespaceschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutnamespaceschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lightsout.techsupport.mk,resources=lightsoutnamespaceschedules/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch

func (r *LightsOutNamespaceScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the schedule
	var schedule lightsoutv1alpha1.LightsOutNamespaceSchedule
	if err := r.Get(ctx, req.NamespacedName, &schedule); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("namespace schedule not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Enrich logger with schedule context for all subsequent log calls
	logger = logger.WithValues("schedule", schedule.Name, "namespace", schedule.Namespace, "generation", schedule.Generation)
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
		logger.Info("namespace schedule is suspended, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Calculate current period
	now := time.Now()
	if r.TimeFunc != nil {
		now = r.TimeFunc()
	}
	timezone := schedule.Spec.Timezone
	if timezone == "" {
		timezone = constants.DefaultTimezone
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

	// Namespace-scoped: only manage the schedule's own namespace
	namespaces := []string{schedule.Namespace}

	logger.Info("reconciling",
		"namespaces", len(namespaces),
		"nextUpscale", period.NextUpscale,
		"nextDownscale", period.NextDownscale)

	// Process workloads
	scaleUp := period.State == "Up"

	// Get rate limit config for requeue calculation
	var rateLimit *lightsoutv1alpha1.RateLimitConfig
	if scaleUp {
		rateLimit = schedule.Spec.UpscaleRateLimit
	} else {
		rateLimit = schedule.Spec.DownscaleRateLimit
	}

	// ArgoCD labeling is ordered relative to workload scaling to prevent false alerts:
	// - Downscale: label ArgoCD apps first, then scale workloads
	// - Upscale: scale workloads first, then remove ArgoCD labels
	if !scaleUp && schedule.Spec.ArgoCD != nil {
		r.labelArgoCDAppsDown(ctx, &schedule, namespaces)
	}

	// Scale all workloads (handles collection, budget-based processing, and metrics)
	scaleResult, err := r.scaleWorkloads(ctx, &schedule, scaleUp)
	if err != nil {
		logger.Error(err, "failed to scale workloads")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	stillWarmingUp := false
	if scaleUp && schedule.Spec.ArgoCD != nil {
		stillWarmingUp = r.handleArgoCDWarmup(ctx, &schedule, namespaces, now)
	}

	stats := scaleResult.stats

	// Update status (LightsOutNamespaceScheduleStatus has no Namespaces field)
	schedule.Status.State = lightsoutv1alpha1.ScheduleState(period.State)
	schedule.Status.WorkloadStats = stats
	schedule.Status.ObservedGeneration = schedule.Generation
	schedule.Status.NextUpscaleTime = &metav1.Time{Time: period.NextUpscale}
	schedule.Status.NextDownscaleTime = &metav1.Time{Time: period.NextDownscale}
	schedule.Status.ScalingProgress = nil
	if scaleResult.batchLimitReached {
		schedule.Status.ScalingProgress = &lightsoutv1alpha1.ScalingProgress{
			Total:      scaleResult.totalWorkloads,
			Completed:  scaleResult.totalProcessed + scaleResult.totalSkipped,
			Failed:     scaleResult.totalFailed,
			InProgress: true,
		}
	}

	// Set Ready condition
	meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            "Successfully reconciled namespace schedule",
		ObservedGeneration: schedule.Generation,
	})

	if err := r.Status().Update(ctx, &schedule); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// Record events for scaling operations
	r.recordScalingEvents(&schedule, scaleUp, stats, namespaces)

	// Record metrics — use "namespace/name" as label to distinguish from global schedules
	scheduleLabel := schedule.Namespace + "/" + schedule.Name
	stateValue := float64(0)
	if stillWarmingUp {
		stateValue = 2
	} else if schedule.Status.State == lightsoutv1alpha1.ScheduleStateUp {
		stateValue = 1
	}
	ScheduleState.WithLabelValues(scheduleLabel).Set(stateValue)

	NextTransitionSeconds.WithLabelValues(scheduleLabel, "upscale").Set(time.Until(period.NextUpscale).Seconds())
	NextTransitionSeconds.WithLabelValues(scheduleLabel, "downscale").Set(time.Until(period.NextDownscale).Seconds())

	ManagedWorkloads.WithLabelValues(scheduleLabel, "deployment").Set(float64(stats.DeploymentsManaged))
	ManagedWorkloads.WithLabelValues(scheduleLabel, "statefulset").Set(float64(stats.StatefulSetsManaged))
	ManagedWorkloads.WithLabelValues(scheduleLabel, "cronjob").Set(float64(stats.CronJobsManaged))

	LastReconcileTime.WithLabelValues(scheduleLabel).SetToCurrentTime()

	// Calculate requeue time
	requeueAfter := calculateRequeueAfter(period, scaleUp, scaleResult, rateLimit, stillWarmingUp, now)

	logger.Info("reconciliation complete", "requeueAfter", requeueAfter, "batchLimitReached", scaleResult.batchLimitReached)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// recordScalingEvents emits Kubernetes events for completed scaling operations.
func (r *LightsOutNamespaceScheduleReconciler) recordScalingEvents(schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule, scaleUp bool, stats lightsoutv1alpha1.WorkloadStats, namespaces []string) {
	if r.Recorder == nil {
		return
	}
	if !scaleUp && stats.DeploymentsScaled+stats.StatefulSetsScaled+stats.CronJobsSuspended > 0 {
		r.Recorder.Eventf(schedule, nil, corev1.EventTypeNormal, constants.EventReasonScaledDown, constants.EventActionScaleDown,
			"Scaled down %d deployments, %d statefulsets, suspended %d cronjobs across %d namespaces",
			stats.DeploymentsScaled, stats.StatefulSetsScaled, stats.CronJobsSuspended, len(namespaces))
	}
	totalManaged := stats.DeploymentsManaged + stats.StatefulSetsManaged + stats.CronJobsManaged
	if scaleUp && totalManaged > 0 {
		r.Recorder.Eventf(schedule, nil, corev1.EventTypeNormal, constants.EventReasonScaledUp, constants.EventActionScaleUp,
			"Scaled up workloads across %d namespaces (managing %d deployments, %d statefulsets, %d cronjobs)",
			len(namespaces), stats.DeploymentsManaged, stats.StatefulSetsManaged, stats.CronJobsManaged)
	}
}

// recordWorkloadEventNS emits a Kubernetes event directly on the given workload object.
// It is a no-op when Recorder is nil, result is nil, result is skipped, or the
// workload's typed pointer field is nil.
func (r *LightsOutNamespaceScheduleReconciler) recordWorkloadEventNS(w Workload, scheduleName string, scaleUp bool, result *ScaleResult) {
	if r.Recorder == nil || result == nil || result.Skipped {
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
			message = fmt.Sprintf("Scaled up by LightsOut namespace schedule %q: replicas %s → %s", scheduleName, result.PreviousValue, result.NewValue)
		} else {
			reason, action = constants.EventReasonScaledDown, constants.EventActionScaleDown
			message = fmt.Sprintf("Scaled down by LightsOut namespace schedule %q: replicas %s → %s", scheduleName, result.PreviousValue, result.NewValue)
		}
	case WorkloadTypeStatefulSet:
		if w.StatefulSet == nil {
			return
		}
		obj = w.StatefulSet
		if scaleUp {
			reason, action = constants.EventReasonScaledUp, constants.EventActionScaleUp
			message = fmt.Sprintf("Scaled up by LightsOut namespace schedule %q: replicas %s → %s", scheduleName, result.PreviousValue, result.NewValue)
		} else {
			reason, action = constants.EventReasonScaledDown, constants.EventActionScaleDown
			message = fmt.Sprintf("Scaled down by LightsOut namespace schedule %q: replicas %s → %s", scheduleName, result.PreviousValue, result.NewValue)
		}
	case WorkloadTypeCronJob:
		if w.CronJob == nil {
			return
		}
		obj = w.CronJob
		if scaleUp {
			reason, action = "Resumed", "Resume"
			message = fmt.Sprintf("Resumed by LightsOut namespace schedule %q", scheduleName)
		} else {
			reason, action = "Suspended", "Suspend"
			message = fmt.Sprintf("Suspended by LightsOut namespace schedule %q", scheduleName)
		}
	default:
		return
	}

	r.Recorder.Eventf(obj, nil, corev1.EventTypeNormal, reason, action, "%s", message)
}

// scaleWorkloads handles the complete scaling workflow using a budget-based
// single-pass approach, scoped to the schedule's own namespace.
func (r *LightsOutNamespaceScheduleReconciler) scaleWorkloads(
	ctx context.Context,
	schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule,
	scaleUp bool,
) (*scaleWorkloadsResult, error) {
	logger := log.FromContext(ctx)

	namespaces := []string{schedule.Namespace}

	// Collect all workloads in own namespace
	workloads, err := r.collectWorkloads(ctx, namespaces, &schedule.Spec.LightsOutScheduleCore, schedule.Name)
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

	// Determine budget: unlimited (-1) if no rate limit, otherwise batchSize
	budget := -1
	if rateLimit != nil && rateLimit.BatchSize != nil && *rateLimit.BatchSize > 0 {
		budget = *rateLimit.BatchSize
	}

	direction := "down"
	if scaleUp {
		direction = "up"
	}

	scheduleLabel := schedule.Namespace + "/" + schedule.Name

	var totalProcessed, totalFailed, totalSkipped int
	startTime := time.Now()

	for i, w := range workloads {
		// Check for context cancellation between workloads for faster shutdown response
		select {
		case <-ctx.Done():
			logger.Info("context cancelled during workload processing, will resume on next reconcile",
				"processed", totalProcessed, "total", len(workloads))
			return &scaleWorkloadsResult{
				stats:          r.buildStatsFromWorkloads(workloads, scaleUp),
				totalProcessed: totalProcessed,
				totalFailed:    totalFailed,
				totalSkipped:   totalSkipped,
			}, ctx.Err()
		default:
		}

		var scaleResult *ScaleResult
		var scaleErr error

		switch w.Type {
		case WorkloadTypeDeployment:
			scaleResult, scaleErr = ScaleDeployment(ctx, r.Client, w.Deployment, schedule.Name, scaleUp)
		case WorkloadTypeStatefulSet:
			scaleResult, scaleErr = ScaleStatefulSet(ctx, r.Client, w.StatefulSet, schedule.Name, scaleUp)
		case WorkloadTypeCronJob:
			scaleResult, scaleErr = ScaleCronJob(ctx, r.Client, w.CronJob, schedule.Name, scaleUp)
		}

		if scaleErr != nil {
			logger.Error(scaleErr, "failed to scale workload", "type", w.Type, "name", w.Name, "namespace", w.Namespace)
			ScalingErrorsTotal.WithLabelValues(scheduleLabel, w.Namespace, string(w.Type)).Inc()
			ScalingWorkloadsProcessed.WithLabelValues(scheduleLabel, direction, "failure").Inc()
			totalFailed++
			continue
		}

		if scaleResult.Skipped {
			totalSkipped++
			continue
		}

		// Actual scale operation performed — consume budget
		operation := "downscale"
		if scaleUp {
			operation = "upscale"
		}
		ScalingOperationsTotal.WithLabelValues(scheduleLabel, w.Namespace, string(w.Type), operation).Inc()
		ScalingWorkloadsProcessed.WithLabelValues(scheduleLabel, direction, "success").Inc()
		totalProcessed++
		r.recordWorkloadEventNS(w, schedule.Name, scaleUp, scaleResult)

		if budget > 0 {
			budget--
			if budget == 0 {
				// Budget exhausted — check if more workloads remain
				moreRemain := i < len(workloads)-1
				if moreRemain {
					ScalingBatchesTotal.WithLabelValues(scheduleLabel, direction).Inc()
					ScalingDurationSeconds.WithLabelValues(scheduleLabel, direction).Observe(time.Since(startTime).Seconds())

					return &scaleWorkloadsResult{
						stats:             r.buildStatsFromWorkloads(workloads, scaleUp),
						totalProcessed:    totalProcessed,
						totalFailed:       totalFailed,
						totalSkipped:      totalSkipped,
						totalWorkloads:    len(workloads),
						batchLimitReached: true,
					}, nil
				}
			}
		}
	}

	// All workloads processed — record metrics
	ScalingDurationSeconds.WithLabelValues(scheduleLabel, direction).Observe(time.Since(startTime).Seconds())
	if totalProcessed > 0 {
		ScalingBatchesTotal.WithLabelValues(scheduleLabel, direction).Inc()
	}

	return &scaleWorkloadsResult{
		stats:          r.buildStatsFromWorkloads(workloads, scaleUp),
		totalProcessed: totalProcessed,
		totalFailed:    totalFailed,
		totalSkipped:   totalSkipped,
	}, nil
}

// collectWorkloads gathers all workloads from the given namespaces.
// When a workload is owned by a different schedule, ownership is transferred to scheduleName
// (preserving the existing original-replicas annotation) so that the namespace schedule
// can take precedence over any global schedule that previously managed the workload.
func (r *LightsOutNamespaceScheduleReconciler) collectWorkloads(ctx context.Context, namespaces []string, core *lightsoutv1alpha1.LightsOutScheduleCore, scheduleName string) ([]Workload, error) {
	var workloads []Workload
	logger := log.FromContext(ctx)

	for _, ns := range namespaces {
		// Collect Deployments
		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeDeployment) {
			var deployments appsv1.DeploymentList
			if err := r.List(ctx, &deployments, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range deployments.Items {
				deploy := &deployments.Items[i]
				excluded, err := ShouldExcludeWorkload(deploy.Labels, core.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "deployment", deploy.Name)
					continue
				}
				if excluded {
					continue
				}
				if existingOwner := deploy.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
					logger.Info("transferring deployment ownership to namespace schedule", "deployment", deploy.Name, "from", existingOwner)
					if deploy.Annotations == nil {
						deploy.Annotations = make(map[string]string)
					}
					deploy.Annotations[constants.ManagedByAnnotation] = scheduleName
					deploy.Labels[constants.ManagedByLabel] = scheduleName
					if updateErr := r.Update(ctx, deploy); updateErr != nil {
						logger.Error(updateErr, "failed to transfer deployment ownership, skipping", "deployment", deploy.Name)
						continue
					}
				}
				workloads = append(workloads, WorkloadFromDeployment(deploy))
			}
		}

		// Collect StatefulSets
		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeStatefulSet) {
			var statefulsets appsv1.StatefulSetList
			if err := r.List(ctx, &statefulsets, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range statefulsets.Items {
				sts := &statefulsets.Items[i]
				excluded, err := ShouldExcludeWorkload(sts.Labels, core.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "statefulset", sts.Name)
					continue
				}
				if excluded {
					continue
				}
				if existingOwner := sts.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
					logger.Info("transferring statefulset ownership to namespace schedule", "statefulset", sts.Name, "from", existingOwner)
					if sts.Annotations == nil {
						sts.Annotations = make(map[string]string)
					}
					sts.Annotations[constants.ManagedByAnnotation] = scheduleName
					sts.Labels[constants.ManagedByLabel] = scheduleName
					if updateErr := r.Update(ctx, sts); updateErr != nil {
						logger.Error(updateErr, "failed to transfer statefulset ownership, skipping", "statefulset", sts.Name)
						continue
					}
				}
				workloads = append(workloads, WorkloadFromStatefulSet(sts))
			}
		}

		// Collect CronJobs
		if shouldProcessWorkloadType(core.WorkloadTypes, lightsoutv1alpha1.WorkloadTypeCronJob) {
			var cronjobs batchv1.CronJobList
			if err := r.List(ctx, &cronjobs, client.InNamespace(ns)); err != nil {
				return nil, err
			}
			for i := range cronjobs.Items {
				cj := &cronjobs.Items[i]
				excluded, err := ShouldExcludeWorkload(cj.Labels, core.ExcludeLabels)
				if err != nil {
					logger.Error(err, "error checking exclusion", "cronjob", cj.Name)
					continue
				}
				if excluded {
					continue
				}
				if existingOwner := cj.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
					logger.Info("transferring cronjob ownership to namespace schedule", "cronjob", cj.Name, "from", existingOwner)
					if cj.Annotations == nil {
						cj.Annotations = make(map[string]string)
					}
					cj.Annotations[constants.ManagedByAnnotation] = scheduleName
					cj.Labels[constants.ManagedByLabel] = scheduleName
					if updateErr := r.Update(ctx, cj); updateErr != nil {
						logger.Error(updateErr, "failed to transfer cronjob ownership, skipping", "cronjob", cj.Name)
						continue
					}
				}
				workloads = append(workloads, WorkloadFromCronJob(cj))
			}
		}
	}

	return workloads, nil
}

// buildStatsFromWorkloads creates WorkloadStats from processed workloads
func (r *LightsOutNamespaceScheduleReconciler) buildStatsFromWorkloads(workloads []Workload, scaleUp bool) lightsoutv1alpha1.WorkloadStats {
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

func (r *LightsOutNamespaceScheduleReconciler) setErrorCondition(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule, err error) {
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

// handleDeletion restores all managed workloads to their original state before allowing deletion
func (r *LightsOutNamespaceScheduleReconciler) handleDeletion(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling deletion, restoring managed workloads")

	var restoreErrors []string
	ns := schedule.Namespace

	// Restore Deployments
	deployments, err := r.listManagedDeployments(ctx, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list deployments in %s: %v", ns, err))
	} else {
		for i := range deployments {
			result, err := ScaleDeployment(ctx, r.Client, &deployments[i], schedule.Name, true)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("deployment %s/%s: %v", ns, deployments[i].Name, err))
			} else {
				r.recordWorkloadEventNS(WorkloadFromDeployment(&deployments[i]), schedule.Name, true, result)
			}
		}
	}

	// Restore StatefulSets
	statefulsets, err := r.listManagedStatefulSets(ctx, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list statefulsets in %s: %v", ns, err))
	} else {
		for i := range statefulsets {
			result, err := ScaleStatefulSet(ctx, r.Client, &statefulsets[i], schedule.Name, true)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("statefulset %s/%s: %v", ns, statefulsets[i].Name, err))
			} else {
				r.recordWorkloadEventNS(WorkloadFromStatefulSet(&statefulsets[i]), schedule.Name, true, result)
			}
		}
	}

	// Restore CronJobs
	cronjobs, err := r.listManagedCronJobs(ctx, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list cronjobs in %s: %v", ns, err))
	} else {
		for i := range cronjobs {
			result, err := ScaleCronJob(ctx, r.Client, &cronjobs[i], schedule.Name, true)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("cronjob %s/%s: %v", ns, cronjobs[i].Name, err))
			} else {
				r.recordWorkloadEventNS(WorkloadFromCronJob(&cronjobs[i]), schedule.Name, true, result)
			}
		}
	}

	// Cleanup ArgoCD Application labels
	if schedule.Spec.ArgoCD != nil {
		namespaces := []string{ns}
		apps, err := DiscoverArgoCDApps(ctx, r.Client, schedule.Spec.ArgoCD, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover ArgoCD apps during cleanup")
		} else {
			for i := range apps {
				if _, err := RemoveArgoCDAppLabels(ctx, r.Client, &apps[i], schedule.Name); err != nil {
					logger.Error(err, "failed to remove labels from ArgoCD app during cleanup", "app", apps[i].GetName())
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

// listManagedDeployments returns deployments managed by the given schedule in the given namespace
func (r *LightsOutNamespaceScheduleReconciler) listManagedDeployments(ctx context.Context, namespace, scheduleName string) ([]appsv1.Deployment, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return deployments.Items, nil
}

// listManagedStatefulSets returns statefulsets managed by the given schedule in the given namespace
func (r *LightsOutNamespaceScheduleReconciler) listManagedStatefulSets(ctx context.Context, namespace, scheduleName string) ([]appsv1.StatefulSet, error) {
	var statefulsets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulsets,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return statefulsets.Items, nil
}

// listManagedCronJobs returns cronjobs managed by the given schedule in the given namespace
func (r *LightsOutNamespaceScheduleReconciler) listManagedCronJobs(ctx context.Context, namespace, scheduleName string) ([]batchv1.CronJob, error) {
	var cronjobs batchv1.CronJobList
	if err := r.List(ctx, &cronjobs,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return cronjobs.Items, nil
}

// labelArgoCDAppsDown discovers and labels ArgoCD apps as down.
// Errors are logged but do not block workload scaling.
func (r *LightsOutNamespaceScheduleReconciler) labelArgoCDAppsDown(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule, namespaces []string) {
	logger := log.FromContext(ctx)

	apps, err := DiscoverArgoCDApps(ctx, r.Client, schedule.Spec.ArgoCD, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover ArgoCD apps, continuing with workload scaling")
		if r.Recorder != nil {
			r.Recorder.Eventf(schedule, nil, corev1.EventTypeWarning, "ArgoCDDiscoveryFailed", "ArgoCD",
				"Failed to discover ArgoCD apps: %v", err)
		}
		return
	}

	for i := range apps {
		if _, err := LabelArgoCDAppDown(ctx, r.Client, &apps[i], schedule.Name); err != nil {
			logger.Error(err, "failed to label ArgoCD app", "app", apps[i].GetName())
		}
	}
}

// handleArgoCDWarmup drives the warming-up state machine for all ArgoCD apps matched
// by the schedule during the Up period.
// Returns true if any app is still in the warming-up state and the reconciler should
// requeue at WarmupCheckInterval.
// Errors are logged but do not block reconciliation.
func (r *LightsOutNamespaceScheduleReconciler) handleArgoCDWarmup(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule, namespaces []string, now time.Time) bool {
	logger := log.FromContext(ctx)

	apps, err := DiscoverArgoCDApps(ctx, r.Client, schedule.Spec.ArgoCD, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover ArgoCD apps for warmup handling")
		if r.Recorder != nil {
			r.Recorder.Eventf(schedule, nil, corev1.EventTypeWarning, "ArgoCDDiscoveryFailed", "ArgoCD",
				"Failed to discover ArgoCD apps: %v", err)
		}
		return false
	}

	warmupTimeout := constants.DefaultWarmupTimeout
	if schedule.Spec.ArgoCD.WarmupTimeout != nil {
		warmupTimeout = schedule.Spec.ArgoCD.WarmupTimeout.Duration
	}

	stillWarmingUp := false

	for i := range apps {
		app := &apps[i]
		labels := app.GetLabels()
		state := labels[constants.StateLabel]

		switch state {
		case constants.StateDown:
			// Workloads just scaled up — transition to warming-up
			if _, err := LabelArgoCDAppWarmingUp(ctx, r.Client, app, schedule.Name, now); err != nil {
				logger.Error(err, "failed to label ArgoCD app as warming-up", "app", app.GetName())
			}
			stillWarmingUp = true

		case constants.StateWarmingUp:
			// Determine when warming-up started; fall back to now if annotation is missing/invalid
			warmingUpSince := now
			annotations := app.GetAnnotations()
			if ts, ok := annotations[constants.WarmingUpSinceAnnotation]; ok {
				if parsed, parseErr := time.Parse(time.RFC3339, ts); parseErr == nil {
					warmingUpSince = parsed
				} else {
					logger.Info("malformed warming-up-since annotation, using current time as fallback",
						"app", app.GetName(), "value", ts)
				}
			}

			timedOut := now.Sub(warmingUpSince) >= warmupTimeout

			destNS, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")

			ready := timedOut
			if !ready && destNS != "" {
				var readErr error
				ready, readErr = CheckWorkloadReadiness(ctx, r.Client, destNS)
				if readErr != nil {
					logger.Error(readErr, "failed to check workload readiness, will retry",
						"app", app.GetName(), "namespace", destNS)
					stillWarmingUp = true
					continue
				}
			} else if !ready {
				logger.Info("ArgoCD app has no destination namespace, warming-up will complete on timeout",
					"app", app.GetName(), "timeout", warmupTimeout)
			}

			if ready {
				if err := CompleteArgoCDWarmup(ctx, r.Client, app); err != nil {
					logger.Error(err, "failed to complete ArgoCD warmup", "app", app.GetName())
					stillWarmingUp = true
				} else if timedOut {
					logger.Info("warmup timeout elapsed, labels removed from ArgoCD app",
						"app", app.GetName(), "timeout", warmupTimeout)
				}
			} else {
				stillWarmingUp = true
			}
		}
	}

	return stillWarmingUp
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightsOutNamespaceScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("lightsout-namespace-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&lightsoutv1alpha1.LightsOutNamespaceSchedule{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("lightsoutnamespaceschedule").
		Complete(r)
}
