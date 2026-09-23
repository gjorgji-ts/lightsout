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
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

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

	// Integrations are ordered around workload scaling to prevent false alerts and
	// reconciliation conflicts:
	// - Downscale: suspend GitOps controllers and turn custom resources off, then scale
	// - Upscale: restore custom resources and wait for them, then scale, then resume GitOps
	scheduleLabel := schedule.Namespace + "/" + schedule.Name
	integrations := integrationConfig{
		ScheduleObj:   &schedule,
		Core:          &schedule.Spec.LightsOutScheduleCore,
		ScheduleName:  schedule.Name,
		ScheduleLabel: scheduleLabel,
		Namespaces:    namespaces,
	}

	deferWorkloadScaleUp := false
	if scaleUp {
		deferWorkloadScaleUp = integrationsUp(ctx, r.Client, r.Recorder, integrations, now)
	} else {
		integrationsDown(ctx, r.Client, r.Recorder, integrations)
	}

	// Scale all workloads (handles collection, budget-based processing, and metrics)
	scaleResult := &scaleWorkloadsResult{}
	if deferWorkloadScaleUp {
		logger.Info("custom resources still warming up, deferring workload scale-up")
	} else {
		scaleResult, err = r.scaleWorkloads(ctx, &schedule, namespaces, scaleUp, rateLimit)
		if err != nil {
			logger.Error(err, "failed to scale workloads")
			r.setErrorCondition(ctx, &schedule, err)
			return ctrl.Result{}, err
		}
	}

	stillWarmingUp := deferWorkloadScaleUp
	if scaleUp && !deferWorkloadScaleUp {
		stillWarmingUp = integrationsWarmup(ctx, r.Client, r.Recorder, integrations, now) || stillWarmingUp
	}

	stats := scaleResult.stats

	// Count what is still running while the schedule is down. Scaling only writes
	// the spec, so this is the one place that notices pods which never go away.
	termination := r.checkTermination(ctx, &schedule, scheduleLabel, namespaces, scaleUp, now)

	// Update status (LightsOutNamespaceScheduleStatus has no Namespaces field)
	schedule.Status.State = lightsoutv1alpha1.ScheduleState(period.State)
	if !deferWorkloadScaleUp {
		// While scale-up is deferred no workloads were visited, so the stats from the
		// downscale still describe reality: everything is down and waiting.
		schedule.Status.WorkloadStats = stats
	}
	schedule.Status.ObservedGeneration = schedule.Generation
	schedule.Status.NextUpscaleTime = &metav1.Time{Time: period.NextUpscale}
	schedule.Status.NextDownscaleTime = &metav1.Time{Time: period.NextDownscale}
	schedule.Status.StuckTerminatingPods = termination.Stuck
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
		Message:            "Successfully reconciled namespace schedule",
		ObservedGeneration: schedule.Generation,
	})

	if err := r.Status().Update(ctx, &schedule); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// Record events for scaling operations. Skipped while scale-up is deferred:
	// no workload was touched, so there is nothing to report.
	if !deferWorkloadScaleUp {
		recordScalingEvents(r.Recorder, &schedule, scaleUp, stats, namespaces)
	}

	// Record metrics - scheduleLabel is "namespace/name" to distinguish from global schedules
	stateValue := float64(0)
	if stillWarmingUp {
		stateValue = 2
	} else if schedule.Status.State == lightsoutv1alpha1.ScheduleStateUp {
		stateValue = 1
	}
	ScheduleState.WithLabelValues(scheduleLabel).Set(stateValue)

	NextTransitionSeconds.WithLabelValues(scheduleLabel, "upscale").Set(time.Until(period.NextUpscale).Seconds())
	NextTransitionSeconds.WithLabelValues(scheduleLabel, "downscale").Set(time.Until(period.NextDownscale).Seconds())

	// Report the persisted stats: while scale-up is deferred the local stats are empty
	// because no workload was visited, but the workloads themselves are still managed.
	reported := schedule.Status.WorkloadStats
	ManagedWorkloads.WithLabelValues(scheduleLabel, "deployment").Set(float64(reported.DeploymentsManaged))
	ManagedWorkloads.WithLabelValues(scheduleLabel, "statefulset").Set(float64(reported.StatefulSetsManaged))
	ManagedWorkloads.WithLabelValues(scheduleLabel, "cronjob").Set(float64(reported.CronJobsManaged))

	LastReconcileTime.WithLabelValues(scheduleLabel).SetToCurrentTime()

	// Calculate requeue time
	requeueAfter := calculateRequeueAfter(period, scaleUp, scaleResult, rateLimit, stillWarmingUp, now)
	requeueAfter = applyTerminationRequeue(requeueAfter, termination, !scaleUp && scaleResult.totalProcessed > 0)

	logger.Info("reconciliation complete", "requeueAfter", requeueAfter, "batchLimitReached", scaleResult.batchLimitReached)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// scaleWorkloads delegates to the shared package-level scaleWorkloads function with
// namespace-schedule-specific configuration (ownership transfer enabled, ns/name as metric label).
func (r *LightsOutNamespaceScheduleReconciler) scaleWorkloads(
	ctx context.Context,
	schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule,
	namespaces []string,
	scaleUp bool,
	rateLimit *lightsoutv1alpha1.RateLimitConfig,
) (*scaleWorkloadsResult, error) {
	cfg := scaleWorkloadsConfig{
		ScheduleName:      schedule.Name,
		ScheduleLabel:     schedule.Namespace + "/" + schedule.Name,
		ScheduleKind:      "namespace schedule",
		Namespaces:        namespaces,
		ScaleUp:           scaleUp,
		RateLimit:         rateLimit,
		TransferOwnership: true,
	}
	return scaleWorkloads(ctx, r.Client, &schedule.Spec.LightsOutScheduleCore, cfg, r.Recorder)
}

// checkTermination counts terminating pods in the managed namespaces and reports
// the ones that have outlived their grace period. See the cluster-scoped
// reconciler's method of the same name for why it only runs while down.
func (r *LightsOutNamespaceScheduleReconciler) checkTermination(
	ctx context.Context,
	schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule,
	scheduleLabel string,
	namespaces []string,
	scaleUp bool,
	now time.Time,
) terminationReport {
	if scaleUp {
		clearStuckTerminatingPods(scheduleLabel, namespaces)
		return terminationReport{}
	}

	report, err := countTerminatingPods(ctx, r.Client, namespaces, now)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to count terminating pods")
		return terminationReport{}
	}
	reportStuckTerminatingPods(ctx, r.Recorder, schedule, scheduleLabel, report)
	return report
}

func (r *LightsOutNamespaceScheduleReconciler) setErrorCondition(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule, err error) {
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
func (r *LightsOutNamespaceScheduleReconciler) handleDeletion(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling deletion, restoring managed workloads")

	var restoreErrors []string
	ns := schedule.Namespace

	hpaList, hpaErr := listHPAs(ctx, r.Client, ns)
	if hpaErr != nil {
		logger.Error(hpaErr, "failed to list HPAs during deletion, HPA restore skipped", "namespace", ns)
	}

	// Restore Deployments
	deployments, err := listManagedDeployments(ctx, r.Client, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list deployments in %s: %v", ns, err))
	} else {
		for i := range deployments {
			result, err := ScaleDeployment(ctx, r.Client, &deployments[i], schedule.Name, true, hpaList)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("deployment %s/%s: %v", ns, deployments[i].Name, err))
			} else {
				recordWorkloadEvent(r.Recorder, WorkloadFromDeployment(&deployments[i]), schedule.Name, "namespace schedule", true, result)
			}
		}
	}

	// Restore StatefulSets
	statefulsets, err := listManagedStatefulSets(ctx, r.Client, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list statefulsets in %s: %v", ns, err))
	} else {
		for i := range statefulsets {
			result, err := ScaleStatefulSet(ctx, r.Client, &statefulsets[i], schedule.Name, true, hpaList)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("statefulset %s/%s: %v", ns, statefulsets[i].Name, err))
			} else {
				recordWorkloadEvent(r.Recorder, WorkloadFromStatefulSet(&statefulsets[i]), schedule.Name, "namespace schedule", true, result)
			}
		}
	}

	// Restore CronJobs
	cronjobs, err := listManagedCronJobs(ctx, r.Client, ns, schedule.Name)
	if err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("list cronjobs in %s: %v", ns, err))
	} else {
		for i := range cronjobs {
			result, err := ScaleCronJob(ctx, r.Client, &cronjobs[i], schedule.Name, true)
			if err != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("cronjob %s/%s: %v", ns, cronjobs[i].Name, err))
			} else {
				recordWorkloadEvent(r.Recorder, WorkloadFromCronJob(&cronjobs[i]), schedule.Name, "namespace schedule", true, result)
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

	// Cleanup FluxCD Kustomization and HelmRelease suspensions
	if schedule.Spec.FluxCD != nil {
		resources, err := DiscoverFluxResources(ctx, r.Client, schedule.Spec.FluxCD, []string{ns})
		if err != nil {
			logger.Error(err, "failed to discover Flux resources during cleanup")
		} else {
			for i := range resources {
				if _, err := ResumeFluxResource(ctx, r.Client, &resources[i], schedule.Name); err != nil {
					logger.Error(err, "failed to resume Flux resource during cleanup", "resource", resources[i].GetName())
				}
			}
		}
	}

	// Restore custom resources this schedule turned off. Resources removed by a
	// `delete` entry are not restored here: rebuilding them is their operator's job
	// once the paused entry alongside them is restored.
	if len(schedule.Spec.CustomResources) > 0 {
		now := time.Now()
		if r.TimeFunc != nil {
			now = r.TimeFunc()
		}
		restoreErrors = append(restoreErrors,
			restoreAllCustomResources(ctx, r.Client, &schedule.Spec.LightsOutScheduleCore, schedule.Name, []string{ns}, now)...)
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

// SetupWithManager sets up the controller with the Manager.
func (r *LightsOutNamespaceScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("lightsout-namespace-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&lightsoutv1alpha1.LightsOutNamespaceSchedule{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Watch HPAs to populate the informer cache so c.List works during reconciliation.
		// HPA changes do not trigger schedule reconciles (no-op handler).
		Watches(hpaWatchObject(), handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, _ client.Object) []reconcile.Request { return nil },
		)).
		Named("lightsoutnamespaceschedule").
		Complete(r)
}
