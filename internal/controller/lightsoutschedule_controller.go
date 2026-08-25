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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

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

	// Discover target namespaces
	targetNamespaces, err := DiscoverNamespaces(ctx, r.Client, &schedule.Spec)
	if err != nil {
		logger.Error(err, "failed to discover namespaces")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	// Release any workloads this schedule still owns in namespaces that have dropped
	// out of its target set (e.g. removed from spec.namespaces). Without this they keep
	// the managed-by annotation/label forever, stranded scaled-down and unclaimable.
	r.releaseOrphanedNamespaces(ctx, &schedule, targetNamespaces)

	// Filter out namespaces that have a LightsOutNamespaceSchedule -
	// namespace-scoped schedules take precedence over this global schedule.
	namespaces, err := FilterNamespacesWithLocalSchedules(ctx, r.Client, targetNamespaces)
	if err != nil {
		logger.Error(err, "failed to filter namespaces with local schedules")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	logger.Info("reconciling",
		"namespaces", len(namespaces),
		"nextUpscale", period.NextUpscale,
		"nextDownscale", period.NextDownscale)

	scaleUp := period.State == "Up"

	// Get rate limit config for requeue calculation
	var rateLimit *lightsoutv1alpha1.RateLimitConfig
	if scaleUp {
		rateLimit = schedule.Spec.UpscaleRateLimit
	} else {
		rateLimit = schedule.Spec.DownscaleRateLimit
	}

	// ArgoCD and FluxCD labeling/suspension is ordered relative to workload scaling
	// to prevent false alerts and reconciliation conflicts:
	// - Downscale: label/suspend integrations first, then scale workloads
	// - Upscale: scale workloads first, then remove ArgoCD labels / resume FluxCD
	if !scaleUp && schedule.Spec.ArgoCD != nil {
		labelArgoCDAppsDown(ctx, r.Client, r.Recorder, &schedule, schedule.Spec.ArgoCD, schedule.Name, namespaces)
	}
	if !scaleUp && schedule.Spec.FluxCD != nil {
		labelFluxResourcesDown(ctx, r.Client, r.Recorder, &schedule, schedule.Spec.FluxCD, schedule.Name, namespaces)
	}

	scaleResult, err := r.scaleWorkloads(ctx, &schedule, namespaces, scaleUp, rateLimit)
	if err != nil {
		logger.Error(err, "failed to scale workloads")
		r.setErrorCondition(ctx, &schedule, err)
		return ctrl.Result{}, err
	}

	stillWarmingUp := false
	if scaleUp && schedule.Spec.ArgoCD != nil {
		stillWarmingUp = handleArgoCDWarmup(ctx, r.Client, r.Recorder, &schedule, schedule.Spec.ArgoCD, schedule.Name, namespaces, now)
	}
	if scaleUp && schedule.Spec.FluxCD != nil {
		fluxWarmingUp := handleFluxCDWarmup(ctx, r.Client, r.Recorder, &schedule, schedule.Spec.FluxCD, schedule.Name, namespaces, now)
		stillWarmingUp = stillWarmingUp || fluxWarmingUp
	}

	stats := scaleResult.stats

	// Update status
	schedule.Status.State = lightsoutv1alpha1.ScheduleState(period.State)
	schedule.Status.Namespaces = namespaces
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
		Type:               constants.ConditionTypeReady,
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
	recordScalingEvents(r.Recorder, &schedule, scaleUp, stats, namespaces)

	// Record metrics
	stateValue := float64(0)
	if stillWarmingUp {
		stateValue = 2
	} else if schedule.Status.State == lightsoutv1alpha1.ScheduleStateUp {
		stateValue = 1
	}
	ScheduleState.WithLabelValues(schedule.Name).Set(stateValue)

	NextTransitionSeconds.WithLabelValues(schedule.Name, "upscale").Set(time.Until(period.NextUpscale).Seconds())
	NextTransitionSeconds.WithLabelValues(schedule.Name, "downscale").Set(time.Until(period.NextDownscale).Seconds())

	ManagedWorkloads.WithLabelValues(schedule.Name, "deployment").Set(float64(stats.DeploymentsManaged))
	ManagedWorkloads.WithLabelValues(schedule.Name, "statefulset").Set(float64(stats.StatefulSetsManaged))
	ManagedWorkloads.WithLabelValues(schedule.Name, "cronjob").Set(float64(stats.CronJobsManaged))

	LastReconcileTime.WithLabelValues(schedule.Name).SetToCurrentTime()

	// Calculate requeue time
	requeueAfter := calculateRequeueAfter(period, scaleUp, scaleResult, rateLimit, stillWarmingUp, now)

	logger.Info("reconciliation complete", "requeueAfter", requeueAfter, "batchLimitReached", scaleResult.batchLimitReached)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// scaleWorkloads delegates to the shared package-level scaleWorkloads function with
// global-schedule-specific configuration (no ownership transfer, schedule name as metric label).
func (r *LightsOutScheduleReconciler) scaleWorkloads(
	ctx context.Context,
	schedule *lightsoutv1alpha1.LightsOutSchedule,
	namespaces []string,
	scaleUp bool,
	rateLimit *lightsoutv1alpha1.RateLimitConfig,
) (*scaleWorkloadsResult, error) {
	cfg := scaleWorkloadsConfig{
		ScheduleName:      schedule.Name,
		ScheduleLabel:     schedule.Name,
		ScheduleKind:      "schedule",
		Namespaces:        namespaces,
		ScaleUp:           scaleUp,
		RateLimit:         rateLimit,
		TransferOwnership: false,
	}
	result, err := scaleWorkloads(ctx, r.Client, &schedule.Spec.LightsOutScheduleCore, cfg, r.Recorder)
	if err != nil {
		return nil, fmt.Errorf("failed to scale workloads: %w", err)
	}
	return result, nil
}

func (r *LightsOutScheduleReconciler) setErrorCondition(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule, err error) {
	meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type:               constants.ConditionTypeReady,
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
func (r *LightsOutScheduleReconciler) handleDeletion(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling deletion, restoring managed workloads")

	// Discover all namespaces this schedule manages
	namespaces, err := DiscoverNamespaces(ctx, r.Client, &schedule.Spec)
	if err != nil {
		logger.Error(err, "failed to discover namespaces during cleanup")
		// Continue with cleanup even if namespace discovery fails
	}

	// Filter out namespaces that have a LightsOutNamespaceSchedule -
	// namespace-scoped schedules manage their own cleanup on deletion.
	namespaces, err = FilterNamespacesWithLocalSchedules(ctx, r.Client, namespaces)
	if err != nil {
		logger.Error(err, "failed to filter namespaces with local schedules")
		return ctrl.Result{}, err
	}

	// Restore every workload and integration resource this schedule owns.
	restoreErrors := r.restoreManagedWorkloads(ctx, schedule, namespaces)

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

// restoreManagedWorkloads restores every workload and integration resource this schedule
// currently owns in the given namespaces to its original, unowned state (replicas restored,
// managed-by annotation/label and ArgoCD/Flux state labels stripped). Shared by finalizer
// cleanup (handleDeletion) and orphan release (releaseOrphanedNamespaces). Returns a slice
// of human-readable errors for resources that failed to restore.
func (r *LightsOutScheduleReconciler) restoreManagedWorkloads(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule, namespaces []string) []string {
	logger := log.FromContext(ctx)
	var restoreErrors []string

	for _, ns := range namespaces {
		hpaList, hpaErr := listHPAs(ctx, r.Client, ns)
		if hpaErr != nil {
			logger.Error(hpaErr, "failed to list HPAs during restore, HPA restore skipped", "namespace", ns)
		}

		deployments, err := listManagedDeployments(ctx, r.Client, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list deployments in %s: %v", ns, err))
		} else {
			for i := range deployments {
				result, err := ScaleDeployment(ctx, r.Client, &deployments[i], schedule.Name, true, hpaList)
				if err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("deployment %s/%s: %v", ns, deployments[i].Name, err))
				} else {
					recordWorkloadEvent(r.Recorder, WorkloadFromDeployment(&deployments[i]), schedule.Name, "schedule", true, result)
				}
			}
		}

		statefulsets, err := listManagedStatefulSets(ctx, r.Client, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list statefulsets in %s: %v", ns, err))
		} else {
			for i := range statefulsets {
				result, err := ScaleStatefulSet(ctx, r.Client, &statefulsets[i], schedule.Name, true, hpaList)
				if err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("statefulset %s/%s: %v", ns, statefulsets[i].Name, err))
				} else {
					recordWorkloadEvent(r.Recorder, WorkloadFromStatefulSet(&statefulsets[i]), schedule.Name, "schedule", true, result)
				}
			}
		}

		cronjobs, err := listManagedCronJobs(ctx, r.Client, ns, schedule.Name)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list cronjobs in %s: %v", ns, err))
		} else {
			for i := range cronjobs {
				result, err := ScaleCronJob(ctx, r.Client, &cronjobs[i], schedule.Name, true)
				if err != nil {
					restoreErrors = append(restoreErrors, fmt.Sprintf("cronjob %s/%s: %v", ns, cronjobs[i].Name, err))
				} else {
					recordWorkloadEvent(r.Recorder, WorkloadFromCronJob(&cronjobs[i]), schedule.Name, "schedule", true, result)
				}
			}
		}
	}

	// Cleanup ArgoCD Application labels
	if schedule.Spec.ArgoCD != nil {
		apps, err := DiscoverArgoCDApps(ctx, r.Client, schedule.Spec.ArgoCD, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover ArgoCD apps during restore")
		} else {
			for i := range apps {
				if _, err := RemoveArgoCDAppLabels(ctx, r.Client, &apps[i], schedule.Name); err != nil {
					logger.Error(err, "failed to remove labels from ArgoCD app during restore", "app", apps[i].GetName())
				}
			}
		}
	}

	// Cleanup FluxCD Kustomization and HelmRelease suspensions
	if schedule.Spec.FluxCD != nil {
		resources, err := DiscoverFluxResources(ctx, r.Client, schedule.Spec.FluxCD, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover Flux resources during restore")
		} else {
			for i := range resources {
				if _, err := ResumeFluxResource(ctx, r.Client, &resources[i], schedule.Name); err != nil {
					logger.Error(err, "failed to resume Flux resource during restore", "resource", resources[i].GetName())
				}
			}
		}
	}

	return restoreErrors
}

// releaseOrphanedNamespaces restores workloads this schedule still owns in namespaces that
// are no longer in its target set. Best-effort: errors are logged, not returned, so they
// never block the main reconcile - the next reconcile retries via optimistic concurrency.
func (r *LightsOutScheduleReconciler) releaseOrphanedNamespaces(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule, targetNamespaces []string) {
	logger := log.FromContext(ctx)

	orphaned, err := r.discoverOrphanedNamespaces(ctx, schedule.Name, targetNamespaces)
	if err != nil {
		logger.Error(err, "failed to discover orphaned namespaces, skipping release")
		return
	}
	if len(orphaned) == 0 {
		return
	}

	logger.Info("releasing workloads in namespaces removed from schedule", "namespaces", orphaned)
	if errs := r.restoreManagedWorkloads(ctx, schedule, orphaned); len(errs) > 0 {
		logger.Error(nil, "failed to release some orphaned workloads", "errors", errs)
	}
}

// discoverOrphanedNamespaces returns namespaces that hold workloads still labeled
// managed-by this schedule but which are no longer in the target set. The managed-by
// label is server-side indexed, so the cluster-wide list is cheap.
//
// Orphaned namespaces are derived from managed workloads (deploy/sts/cj) only.
// An ArgoCD/Flux resource whose destination namespace has zero managed workloads would not
// be swept here (its labels linger until the namespace is re-added or the schedule deleted).
// We will add per-integration cluster-wide label scans if that edge case shows up in practice.
func (r *LightsOutScheduleReconciler) discoverOrphanedNamespaces(ctx context.Context, scheduleName string, targetNamespaces []string) ([]string, error) {
	active := make(map[string]struct{}, len(targetNamespaces))
	for _, ns := range targetNamespaces {
		active[ns] = struct{}{}
	}

	orphaned := make(map[string]struct{})
	add := func(ns string) {
		if _, ok := active[ns]; !ok {
			orphaned[ns] = struct{}{}
		}
	}

	sel := client.MatchingLabels{constants.ManagedByLabel: scheduleName}

	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys, sel); err != nil {
		return nil, err
	}
	for i := range deploys.Items {
		add(deploys.Items[i].Namespace)
	}

	var statefulsets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulsets, sel); err != nil {
		return nil, err
	}
	for i := range statefulsets.Items {
		add(statefulsets.Items[i].Namespace)
	}

	var cronjobs batchv1.CronJobList
	if err := r.List(ctx, &cronjobs, sel); err != nil {
		return nil, err
	}
	for i := range cronjobs.Items {
		add(cronjobs.Items[i].Namespace)
	}

	result := make([]string, 0, len(orphaned))
	for ns := range orphaned {
		result = append(result, ns)
	}
	slices.Sort(result)
	return result, nil
}

// calculateRequeueAfter returns how long to wait before the next reconciliation.
// Batch-limited runs requeue after the batch delay (capped at the next transition).
// Warming-up runs requeue at WarmupCheckInterval. Otherwise, requeue at the next transition.
func calculateRequeueAfter(period *PeriodResult, scaleUp bool, scaleResult *scaleWorkloadsResult, rateLimit *lightsoutv1alpha1.RateLimitConfig, stillWarmingUp bool, now time.Time) time.Duration {
	var timeUntilNext time.Duration
	if scaleUp {
		timeUntilNext = period.NextDownscale.Sub(now)
	} else {
		timeUntilNext = period.NextUpscale.Sub(now)
	}

	if scaleResult.batchLimitReached {
		// More workloads to process - requeue after batch delay, but not later
		// than the next period transition (so we don't miss direction changes).
		requeueAfter := timeUntilNext
		if rateLimit != nil && rateLimit.DelayBetweenBatches != nil {
			if delay := rateLimit.DelayBetweenBatches.Duration; delay < requeueAfter {
				requeueAfter = delay
			}
		}
		return max(requeueAfter, time.Second)
	}
	if stillWarmingUp {
		// ArgoCD or FluxCD resources are warming up poll readiness at WarmupCheckInterval,
		// but no later than the next period transition.
		return max(min(constants.WarmupCheckInterval, timeUntilNext), time.Second)
	}
	// Defensive floor for the idle path - next transition is typically hours away.
	return max(timeUntilNext, time.Minute)
}

func shouldProcessWorkloadType(types []lightsoutv1alpha1.WorkloadType, target lightsoutv1alpha1.WorkloadType) bool {
	if len(types) == 0 {
		return true // Process all types if none specified
	}
	return slices.Contains(types, target)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightsOutScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("lightsout-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&lightsoutv1alpha1.LightsOutSchedule{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Watch HPAs to populate the informer cache so c.List works during reconciliation.
		// HPA changes do not trigger schedule reconciles (no-op handler).
		Watches(hpaWatchObject(), handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, _ client.Object) []reconcile.Request { return nil },
		)).
		Named("lightsoutschedule").
		Complete(r)
}
