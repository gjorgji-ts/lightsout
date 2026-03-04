package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	stats             lightsoutv1alpha1.WorkloadStats
	totalProcessed    int
	totalFailed       int
	totalSkipped      int
	totalWorkloads    int // full collection size; set only when batchLimitReached is true
	batchLimitReached bool
}

// scaleWorkloadsConfig carries per-reconciler parameters into the shared scaleWorkloads function.
type scaleWorkloadsConfig struct {
	// ScheduleName is the name of the schedule CR (used for label-based lookups and events).
	ScheduleName string
	// ScheduleLabel is the Prometheus metric label. Global schedules use the name; namespace
	// schedules use "namespace/name" to avoid collisions.
	ScheduleLabel string
	// ScheduleKind is the human-readable schedule type used in event messages
	// ("schedule" for global, "namespace schedule" for NS).
	ScheduleKind string
	// Namespaces is the set of namespaces to process.
	Namespaces []string
	// ScaleUp indicates the desired direction.
	ScaleUp bool
	// RateLimit configures optional batching; nil means unlimited.
	RateLimit *lightsoutv1alpha1.RateLimitConfig
	// TransferOwnership, when true, re-stamps workloads that carry a foreign managed-by
	// label so the caller's schedule takes precedence (used by namespace schedules).
	TransferOwnership bool
}

// buildStatsFromWorkloads creates WorkloadStats from a collected workload slice.
func buildStatsFromWorkloads(workloads []Workload, scaleUp bool) lightsoutv1alpha1.WorkloadStats {
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
		if transferOwnership {
			if existingOwner := deploy.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring deployment ownership to namespace schedule", "deployment", deploy.Name, "from", existingOwner)
				if deploy.Annotations == nil {
					deploy.Annotations = make(map[string]string)
				}
				deploy.Annotations[constants.ManagedByAnnotation] = scheduleName
				deploy.Labels[constants.ManagedByLabel] = scheduleName
				if updateErr := c.Update(ctx, deploy); updateErr != nil {
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
		if transferOwnership {
			if existingOwner := sts.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring statefulset ownership to namespace schedule", "statefulset", sts.Name, "from", existingOwner)
				if sts.Annotations == nil {
					sts.Annotations = make(map[string]string)
				}
				sts.Annotations[constants.ManagedByAnnotation] = scheduleName
				sts.Labels[constants.ManagedByLabel] = scheduleName
				if updateErr := c.Update(ctx, sts); updateErr != nil {
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
		if transferOwnership {
			if existingOwner := cj.Labels[constants.ManagedByLabel]; existingOwner != "" && existingOwner != scheduleName {
				logger.Info("transferring cronjob ownership to namespace schedule", "cronjob", cj.Name, "from", existingOwner)
				if cj.Annotations == nil {
					cj.Annotations = make(map[string]string)
				}
				cj.Annotations[constants.ManagedByAnnotation] = scheduleName
				cj.Labels[constants.ManagedByLabel] = scheduleName
				if updateErr := c.Update(ctx, cj); updateErr != nil {
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
// Skipped workloads (already at target state) do not consume budget, making re-entry
// after requeue cheap — the reconciler naturally picks up where it left off via
// annotation-based idempotency.
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

	// Determine budget: unlimited (-1) if no rate limit, otherwise batchSize
	budget := -1
	if cfg.RateLimit != nil && cfg.RateLimit.BatchSize != nil && *cfg.RateLimit.BatchSize > 0 {
		budget = *cfg.RateLimit.BatchSize
	}

	direction := "down"
	if cfg.ScaleUp {
		direction = "up"
	}

	var totalProcessed, totalFailed, totalSkipped int
	startTime := time.Now()

	for i, w := range workloads {
		// Check for context cancellation between workloads for faster shutdown response
		select {
		case <-ctx.Done():
			logger.Info("context cancelled during workload processing, will resume on next reconcile",
				"processed", totalProcessed, "total", len(workloads))
			return &scaleWorkloadsResult{
				stats:          buildStatsFromWorkloads(workloads, cfg.ScaleUp),
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
			scaleResult, scaleErr = ScaleDeployment(ctx, c, w.Deployment, cfg.ScheduleName, cfg.ScaleUp)
		case WorkloadTypeStatefulSet:
			scaleResult, scaleErr = ScaleStatefulSet(ctx, c, w.StatefulSet, cfg.ScheduleName, cfg.ScaleUp)
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
						stats:             buildStatsFromWorkloads(workloads, cfg.ScaleUp),
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
	ScalingDurationSeconds.WithLabelValues(cfg.ScheduleLabel, direction).Observe(time.Since(startTime).Seconds())
	if totalProcessed > 0 {
		ScalingBatchesTotal.WithLabelValues(cfg.ScheduleLabel, direction).Inc()
	}

	return &scaleWorkloadsResult{
		stats:          buildStatsFromWorkloads(workloads, cfg.ScaleUp),
		totalProcessed: totalProcessed,
		totalFailed:    totalFailed,
		totalSkipped:   totalSkipped,
	}, nil
}
