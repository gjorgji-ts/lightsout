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
	"math"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// Skip reasons reported on ScaleResult when a workload is not scaled.
const (
	skipReasonDifferentSchedule = "managed by different schedule"
	skipReasonNotManaged        = "not managed by lightsout"
	// skipReasonUserParked marks a workload a user left at zero replicas. LightsOut
	// never claims one, so nothing waits for it either.
	skipReasonUserParked = "already at 0 replicas"
)

// ScaleResult contains the result of a scaling operation
type ScaleResult struct {
	Skipped       bool
	SkipReason    string
	PreviousValue string
	NewValue      string
}

// ScaleDeployment scales a deployment up or down based on the period.
// scaleUp=true means restore to original replicas, scaleUp=false means scale to 0.
// hpaList is the pre-fetched HPA list for the deployment's namespace. Pass nil to skip HPA handling.
func ScaleDeployment(ctx context.Context, c client.Client, deploy *appsv1.Deployment, scheduleName string, scaleUp bool, hpaList *unstructured.UnstructuredList) (*ScaleResult, error) {
	return scaleReplicas(ctx, c, deploy, "Deployment", deploy.Spec.Replicas, scheduleName, scaleUp, hpaList)
}

// ScaleStatefulSet scales a statefulset up or down based on the period.
// scaleUp=true means restore to original replicas, scaleUp=false means scale to 0.
// hpaList is the pre-fetched HPA list for the statefulset's namespace. Pass nil to skip HPA handling.
func ScaleStatefulSet(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, scheduleName string, scaleUp bool, hpaList *unstructured.UnstructuredList) (*ScaleResult, error) {
	return scaleReplicas(ctx, c, sts, "StatefulSet", sts.Spec.Replicas, scheduleName, scaleUp, hpaList)
}

// scaleReplicas holds the scaling logic shared by Deployments and StatefulSets. The two
// differ only in their kind, so obj is the typed object and replicas is its Spec.Replicas.
func scaleReplicas(ctx context.Context, c client.Client, obj client.Object, kind string, replicas *int32, scheduleName string, scaleUp bool, hpaList *unstructured.UnstructuredList) (*ScaleResult, error) {
	logger := log.FromContext(ctx).WithValues(strings.ToLower(kind), obj.GetName(), "namespace", obj.GetNamespace())

	annotations := obj.GetAnnotations()
	managedBy := annotations[constants.ManagedByAnnotation]
	originalReplicas := annotations[constants.OriginalReplicasAnnotation]

	if scaleUp {
		return scaleReplicasUp(ctx, c, obj, kind, scheduleName, managedBy, originalReplicas, hpaList, logger)
	}
	return scaleReplicasDown(ctx, c, obj, kind, replicas, scheduleName, managedBy, originalReplicas, hpaList, logger)
}

func scaleReplicasDown(ctx context.Context, c client.Client, obj client.Object, kind string, replicas *int32, scheduleName, managedBy, originalReplicas string, hpaList *unstructured.UnstructuredList, logger logr.Logger) (*ScaleResult, error) {
	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping workload: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: skipReasonDifferentSchedule}, nil
	}

	// Skip if already scaled down (has original-replicas annotation)
	if originalReplicas != "" {
		logger.V(1).Info("skipping workload: already scaled down")
		return &ScaleResult{Skipped: true, SkipReason: "already scaled down"}, nil
	}

	// Get the current replica count. A nil Spec.Replicas defaults to 1 in Kubernetes,
	// and this mirrors that, to capture the effective count.
	//
	// Replicas explicitly at 0 with no annotation is user-managed, such as a
	// maintenance mode. Skip it: a workload the user parked at zero is not ours
	// to claim.
	currentReplicas := ptr.Deref(replicas, 1)
	if currentReplicas == 0 {
		logger.V(1).Info("skipping workload: already at 0 replicas (user-managed)")
		return &ScaleResult{Skipped: true, SkipReason: skipReasonUserParked}, nil
	}

	// Disable HPA scale-up before zeroing replicas to prevent HPA fight-back.
	// Non-fatal: log the error and continue so workload scaling is never blocked.
	if hpaErr := PatchHPAForDownscale(ctx, c, hpaList, obj.GetNamespace(), kind, obj.GetName(), scheduleName); hpaErr != nil {
		logger.Error(hpaErr, "failed to patch HPA for downscale, continuing")
	}

	// Scale down.
	//
	// This patches rather than updates. The workload comes from a List, and an
	// operator that owns it rewrites its status continuously. The copy in hand is
	// therefore often one version behind, and Update fails with "the object has been
	// modified". A merge patch carries no resourceVersion, so it cannot conflict.
	if err := applyMergePatch(ctx, c, obj, objectPatch{
		SetAnnotations: map[string]string{
			constants.OriginalReplicasAnnotation: strconv.Itoa(int(currentReplicas)),
			constants.ManagedByAnnotation:        scheduleName,
		},
		SetLabels: map[string]string{constants.ManagedByLabel: scheduleName},
		Spec:      map[string]any{fieldReplicas: 0},
	}); err != nil {
		return nil, err
	}

	logger.Info("scaled down workload", "from", currentReplicas, "to", 0)
	return &ScaleResult{
		PreviousValue: strconv.Itoa(int(currentReplicas)),
		NewValue:      "0",
	}, nil
}

func scaleReplicasUp(ctx context.Context, c client.Client, obj client.Object, kind, scheduleName, managedBy, originalReplicas string, hpaList *unstructured.UnstructuredList, logger logr.Logger) (*ScaleResult, error) {
	// Skip if no original-replicas annotation (not managed by us).
	// Still attempt the HPA restore. A controller that crashed after the workload
	// update but before RestoreHPA ran leaves the HPA with scaleUp disabled and
	// dangling annotations. RestoreHPA is a no-op on an HPA with no managed-by
	// annotation, which is the genuine skip case.
	if originalReplicas == "" {
		if hpaErr := RestoreHPA(ctx, c, hpaList, obj.GetNamespace(), kind, obj.GetName(), scheduleName); hpaErr != nil {
			logger.Error(hpaErr, "failed to restore HPA during skip check, continuing")
		}
		logger.V(1).Info("skipping workload: no original-replicas annotation")
		return &ScaleResult{Skipped: true, SkipReason: skipReasonNotManaged}, nil
	}

	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping workload: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: skipReasonDifferentSchedule}, nil
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
	if err := applyMergePatch(ctx, c, obj, objectPatch{
		RemoveAnnotations: []string{constants.OriginalReplicasAnnotation, constants.ManagedByAnnotation},
		RemoveLabels:      []string{constants.ManagedByLabel},
		Spec:              map[string]any{fieldReplicas: replicasInt32},
	}); err != nil {
		return nil, err
	}

	// Restore HPA minReplicas after replica write. Non-fatal.
	if hpaErr := RestoreHPA(ctx, c, hpaList, obj.GetNamespace(), kind, obj.GetName(), scheduleName); hpaErr != nil {
		logger.Error(hpaErr, "failed to restore HPA after upscale, continuing")
	}

	logger.Info("scaled up workload", "from", 0, "to", replicas)
	return &ScaleResult{
		PreviousValue: "0",
		NewValue:      strconv.FormatInt(replicas, 10),
	}, nil
}

// listManagedDeployments returns all Deployments in namespace carrying the managed-by label for scheduleName.
func listManagedDeployments(ctx context.Context, c client.Client, namespace, scheduleName string) ([]appsv1.Deployment, error) {
	var list appsv1.DeploymentList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// listManagedStatefulSets returns all StatefulSets in namespace carrying the managed-by label for scheduleName.
func listManagedStatefulSets(ctx context.Context, c client.Client, namespace, scheduleName string) ([]appsv1.StatefulSet, error) {
	var list appsv1.StatefulSetList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// listManagedCronJobs returns all CronJobs in namespace carrying the managed-by label for scheduleName.
func listManagedCronJobs(ctx context.Context, c client.Client, namespace, scheduleName string) ([]batchv1.CronJob, error) {
	var list batchv1.CronJobList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.ManagedByLabel: scheduleName},
	); err != nil {
		return nil, err
	}
	return list.Items, nil
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
		return &ScaleResult{Skipped: true, SkipReason: skipReasonDifferentSchedule}, nil
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

	// Suspend the cronjob. Patched rather than updated, for the reason given in scaleReplicasDown.
	if err := applyMergePatch(ctx, c, cj, objectPatch{
		SetAnnotations: map[string]string{
			constants.OriginalSuspendAnnotation: constants.SuspendedByLightsOut,
			constants.ManagedByAnnotation:       scheduleName,
		},
		SetLabels: map[string]string{constants.ManagedByLabel: scheduleName},
		Spec:      map[string]any{fieldSuspend: true},
	}); err != nil {
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
		if err := applyMergePatch(ctx, c, cj, objectPatch{
			SetAnnotations: map[string]string{constants.OriginalSuspendAnnotation: constants.SuspendedByUser},
		}); err != nil {
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
		return &ScaleResult{Skipped: true, SkipReason: skipReasonNotManaged}, nil
	}

	// Skip if managed by different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.Info("skipping cronjob: managed by different schedule", "managedBy", managedBy)
		return &ScaleResult{Skipped: true, SkipReason: skipReasonDifferentSchedule}, nil
	}

	// Resume the cronjob
	if err := applyMergePatch(ctx, c, cj, objectPatch{
		RemoveAnnotations: []string{constants.OriginalSuspendAnnotation, constants.ManagedByAnnotation},
		RemoveLabels:      []string{constants.ManagedByLabel},
		Spec:              map[string]any{fieldSuspend: false},
	}); err != nil {
		return nil, err
	}

	logger.Info("resumed cronjob")
	return &ScaleResult{
		PreviousValue: "true",
		NewValue:      "false",
	}, nil
}
