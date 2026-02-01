package controller

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// ScaleResult contains the result of a scaling operation
type ScaleResult struct {
	Skipped       bool
	SkipReason    string
	PreviousValue string
	NewValue      string
}

// ScaleDeployment scales a deployment up or down based on the period
// scaleUp=true means restore to original replicas, scaleUp=false means scale to 0
func ScaleDeployment(ctx context.Context, c client.Client, deploy *appsv1.Deployment, scheduleName string, scaleUp bool) (*ScaleResult, error) {
	logger := log.FromContext(ctx).WithValues("deployment", deploy.Name, "namespace", deploy.Namespace)

	if deploy.Annotations == nil {
		deploy.Annotations = make(map[string]string)
	}

	managedBy := deploy.Annotations[constants.ManagedByAnnotation]
	originalReplicas := deploy.Annotations[constants.OriginalReplicasAnnotation]

	if scaleUp {
		return scaleDeploymentUp(ctx, c, deploy, scheduleName, managedBy, originalReplicas, logger)
	}
	return scaleDeploymentDown(ctx, c, deploy, scheduleName, managedBy, originalReplicas, logger)
}

func scaleDeploymentDown(ctx context.Context, c client.Client, deploy *appsv1.Deployment, scheduleName, managedBy, originalReplicas string, logger logr.Logger) (*ScaleResult, error) {
	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping deployment: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Skip if already scaled down (has original-replicas annotation)
	if originalReplicas != "" {
		logger.V(1).Info("skipping deployment: already scaled down")
		return &ScaleResult{Skipped: true, SkipReason: "already scaled down"}, nil
	}

	// Get current replica count. If Spec.Replicas is nil, Kubernetes defaults to 1.
	// We mirror this behavior to correctly capture the effective replica count.
	// If replicas is explicitly 0 (no annotation), this is user-managed (e.g., maintenance mode)
	// and we skip it to preserve user intent - we don't want to claim ownership of workloads
	// the user has intentionally scaled down.
	currentReplicas := int32(1)
	if deploy.Spec.Replicas != nil {
		currentReplicas = *deploy.Spec.Replicas
	}
	if currentReplicas == 0 {
		logger.V(1).Info("skipping deployment: already at 0 replicas (user-managed)")
		return &ScaleResult{Skipped: true, SkipReason: "already at 0 replicas"}, nil
	}

	// Scale down
	deploy.Annotations[constants.OriginalReplicasAnnotation] = strconv.Itoa(int(currentReplicas))
	deploy.Annotations[constants.ManagedByAnnotation] = scheduleName
	if deploy.Labels == nil {
		deploy.Labels = make(map[string]string)
	}
	deploy.Labels[constants.ManagedByLabel] = scheduleName
	zero := int32(0)
	deploy.Spec.Replicas = &zero

	if err := c.Update(ctx, deploy); err != nil {
		return nil, err
	}

	logger.Info("scaled down deployment", "from", currentReplicas, "to", 0)
	return &ScaleResult{
		PreviousValue: strconv.Itoa(int(currentReplicas)),
		NewValue:      "0",
	}, nil
}

func scaleDeploymentUp(ctx context.Context, c client.Client, deploy *appsv1.Deployment, scheduleName, managedBy, originalReplicas string, logger logr.Logger) (*ScaleResult, error) {
	// Skip if no original-replicas annotation (not managed by us)
	if originalReplicas == "" {
		logger.V(1).Info("skipping deployment: no original-replicas annotation")
		return &ScaleResult{Skipped: true, SkipReason: "not managed by lightsout"}, nil
	}

	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping deployment: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Parse original replicas
	replicas, err := strconv.ParseInt(originalReplicas, 10, 32)
	if err != nil {
		logger.Info("invalid original-replicas annotation, defaulting to 1", "value", originalReplicas)
		replicas = 1
	}

	// Scale up
	if replicas < math.MinInt32 || replicas > math.MaxInt32 {
		return nil, fmt.Errorf("original replicas value %d overflows int32", replicas)
	}
	replicasInt32 := int32(replicas)
	deploy.Spec.Replicas = &replicasInt32
	delete(deploy.Annotations, constants.OriginalReplicasAnnotation)
	delete(deploy.Annotations, constants.ManagedByAnnotation)
	delete(deploy.Labels, constants.ManagedByLabel)

	if err := c.Update(ctx, deploy); err != nil {
		return nil, err
	}

	logger.Info("scaled up deployment", "from", 0, "to", replicas)
	return &ScaleResult{
		PreviousValue: "0",
		NewValue:      strconv.FormatInt(replicas, 10),
	}, nil
}

// ScaleStatefulSet scales a statefulset up or down based on the period
// scaleUp=true means restore to original replicas, scaleUp=false means scale to 0
func ScaleStatefulSet(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, scheduleName string, scaleUp bool) (*ScaleResult, error) {
	logger := log.FromContext(ctx).WithValues("statefulset", sts.Name, "namespace", sts.Namespace)

	if sts.Annotations == nil {
		sts.Annotations = make(map[string]string)
	}

	managedBy := sts.Annotations[constants.ManagedByAnnotation]
	originalReplicas := sts.Annotations[constants.OriginalReplicasAnnotation]

	if scaleUp {
		return scaleStatefulSetUp(ctx, c, sts, scheduleName, managedBy, originalReplicas, logger)
	}
	return scaleStatefulSetDown(ctx, c, sts, scheduleName, managedBy, originalReplicas, logger)
}

func scaleStatefulSetDown(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, scheduleName, managedBy, originalReplicas string, logger logr.Logger) (*ScaleResult, error) {
	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping statefulset: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Skip if already scaled down (has original-replicas annotation)
	if originalReplicas != "" {
		logger.V(1).Info("skipping statefulset: already scaled down")
		return &ScaleResult{Skipped: true, SkipReason: "already scaled down"}, nil
	}

	// Get current replica count. If Spec.Replicas is nil, Kubernetes defaults to 1.
	// We mirror this behavior to correctly capture the effective replica count.
	// If replicas is explicitly 0 (no annotation), this is user-managed (e.g., maintenance mode)
	// and we skip it to preserve user intent - we don't want to claim ownership of workloads
	// the user has intentionally scaled down.
	currentReplicas := int32(1)
	if sts.Spec.Replicas != nil {
		currentReplicas = *sts.Spec.Replicas
	}
	if currentReplicas == 0 {
		logger.V(1).Info("skipping statefulset: already at 0 replicas (user-managed)")
		return &ScaleResult{Skipped: true, SkipReason: "already at 0 replicas"}, nil
	}

	// Scale down
	sts.Annotations[constants.OriginalReplicasAnnotation] = strconv.Itoa(int(currentReplicas))
	sts.Annotations[constants.ManagedByAnnotation] = scheduleName
	if sts.Labels == nil {
		sts.Labels = make(map[string]string)
	}
	sts.Labels[constants.ManagedByLabel] = scheduleName
	zero := int32(0)
	sts.Spec.Replicas = &zero

	if err := c.Update(ctx, sts); err != nil {
		return nil, err
	}

	logger.Info("scaled down statefulset", "from", currentReplicas, "to", 0)
	return &ScaleResult{
		PreviousValue: strconv.Itoa(int(currentReplicas)),
		NewValue:      "0",
	}, nil
}

func scaleStatefulSetUp(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, scheduleName, managedBy, originalReplicas string, logger logr.Logger) (*ScaleResult, error) {
	// Skip if no original-replicas annotation (not managed by us)
	if originalReplicas == "" {
		logger.V(1).Info("skipping statefulset: no original-replicas annotation")
		return &ScaleResult{Skipped: true, SkipReason: "not managed by lightsout"}, nil
	}

	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping statefulset: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Parse original replicas
	replicas, err := strconv.ParseInt(originalReplicas, 10, 32)
	if err != nil {
		logger.Info("invalid original-replicas annotation, defaulting to 1", "value", originalReplicas)
		replicas = 1
	}

	// Scale up
	if replicas < math.MinInt32 || replicas > math.MaxInt32 {
		return nil, fmt.Errorf("original replicas value %d overflows int32", replicas)
	}
	replicasInt32 := int32(replicas)
	sts.Spec.Replicas = &replicasInt32
	delete(sts.Annotations, constants.OriginalReplicasAnnotation)
	delete(sts.Annotations, constants.ManagedByAnnotation)
	delete(sts.Labels, constants.ManagedByLabel)

	if err := c.Update(ctx, sts); err != nil {
		return nil, err
	}

	logger.Info("scaled up statefulset", "from", 0, "to", replicas)
	return &ScaleResult{
		PreviousValue: "0",
		NewValue:      strconv.FormatInt(replicas, 10),
	}, nil
}

// ScaleCronJob suspends or resumes a cronjob based on the period
func ScaleCronJob(ctx context.Context, c client.Client, cj *batchv1.CronJob, scheduleName string, scaleUp bool) (*ScaleResult, error) {
	logger := log.FromContext(ctx).WithValues("cronjob", cj.Name, "namespace", cj.Namespace)

	if cj.Annotations == nil {
		cj.Annotations = make(map[string]string)
	}

	managedBy := cj.Annotations[constants.ManagedByAnnotation]
	originalSuspend := cj.Annotations[constants.OriginalSuspendAnnotation]

	if scaleUp {
		return scaleCronJobUp(ctx, c, cj, scheduleName, managedBy, originalSuspend, logger)
	}
	return scaleCronJobDown(ctx, c, cj, scheduleName, managedBy, originalSuspend, logger)
}

func scaleCronJobDown(ctx context.Context, c client.Client, cj *batchv1.CronJob, scheduleName, managedBy, originalSuspend string, logger logr.Logger) (*ScaleResult, error) {
	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping cronjob: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Skip if already has annotation (already managed)
	if originalSuspend != "" {
		logger.V(1).Info("skipping cronjob: already managed")
		return &ScaleResult{Skipped: true, SkipReason: "already managed"}, nil
	}

	// Check current suspend state
	isSuspended := cj.Spec.Suspend != nil && *cj.Spec.Suspend

	// If already suspended without annotation, it's user-managed - skip
	if isSuspended {
		logger.V(1).Info("skipping cronjob: already suspended by user")
		return &ScaleResult{Skipped: true, SkipReason: "suspended by user"}, nil
	}

	// Suspend the cronjob
	cj.Annotations[constants.OriginalSuspendAnnotation] = constants.SuspendedByLightsOut
	cj.Annotations[constants.ManagedByAnnotation] = scheduleName
	if cj.Labels == nil {
		cj.Labels = make(map[string]string)
	}
	cj.Labels[constants.ManagedByLabel] = scheduleName
	suspend := true
	cj.Spec.Suspend = &suspend

	if err := c.Update(ctx, cj); err != nil {
		return nil, err
	}

	logger.Info("suspended cronjob")
	return &ScaleResult{
		PreviousValue: "false",
		NewValue:      "true",
	}, nil
}

func scaleCronJobUp(ctx context.Context, c client.Client, cj *batchv1.CronJob, scheduleName, managedBy, originalSuspend string, logger logr.Logger) (*ScaleResult, error) {
	isSuspended := cj.Spec.Suspend != nil && *cj.Spec.Suspend

	// If suspended without annotation, mark as user-owned and skip
	if isSuspended && originalSuspend == "" {
		cj.Annotations[constants.OriginalSuspendAnnotation] = constants.SuspendedByUser
		if err := c.Update(ctx, cj); err != nil {
			return nil, err
		}
		logger.Info("marked cronjob as user-suspended")
		return &ScaleResult{Skipped: true, SkipReason: "marked as user-suspended"}, nil
	}

	// If marked as user-suspended, never resume
	if originalSuspend == constants.SuspendedByUser {
		logger.V(1).Info("skipping cronjob: user-suspended")
		return &ScaleResult{Skipped: true, SkipReason: "user-suspended"}, nil
	}

	// If not managed by us, skip
	if originalSuspend != constants.SuspendedByLightsOut {
		logger.V(1).Info("skipping cronjob: not managed by lightsout")
		return &ScaleResult{Skipped: true, SkipReason: "not managed by lightsout"}, nil
	}

	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping cronjob: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: "managed by different schedule"}, nil
	}

	// Resume the cronjob
	suspend := false
	cj.Spec.Suspend = &suspend
	delete(cj.Annotations, constants.OriginalSuspendAnnotation)
	delete(cj.Annotations, constants.ManagedByAnnotation)
	delete(cj.Labels, constants.ManagedByLabel)

	if err := c.Update(ctx, cj); err != nil {
		return nil, err
	}

	logger.Info("resumed cronjob")
	return &ScaleResult{
		PreviousValue: "true",
		NewValue:      "false",
	}, nil
}
