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
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// capturedField records what a field looked like before downscale overwrote it.
// Present distinguishes a field that held a value from one that did not exist,
// so restore knows whether to write the old value back or remove the field.
type capturedField struct {
	Present bool `json:"present"`
	Value   any  `json:"value,omitempty"`
}

// customResourceGVK returns the GroupVersionKind a config targets.
func customResourceGVK(cfg *lightsoutv1alpha1.CustomResourceConfig) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: cfg.Group, Version: cfg.Version, Kind: cfg.Kind}
}

// DiscoverCustomResources lists every resource of the configured kind in the given
// namespaces that matches the config's name and label filters.
//
// A missing CRD is not an error: like the ArgoCD and FluxCD integrations, lightsout
// runs on clusters where the operator simply is not installed, so discovery returns
// empty and scaling proceeds.
func DiscoverCustomResources(
	ctx context.Context,
	c client.Client,
	cfg *lightsoutv1alpha1.CustomResourceConfig,
	namespaces []string,
) ([]unstructured.Unstructured, error) {
	logger := log.FromContext(ctx)
	gvk := customResourceGVK(cfg)
	listGVK := gvk
	listGVK.Kind += "List"

	var matched []unstructured.Unstructured

	for _, ns := range namespaces {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK)

		opts := []client.ListOption{client.InNamespace(ns)}
		if len(cfg.MatchLabels) > 0 {
			opts = append(opts, client.MatchingLabels(cfg.MatchLabels))
		}

		if err := c.List(ctx, list, opts...); err != nil {
			if meta.IsNoMatchError(err) {
				logger.Info("custom resource CRD not found on cluster, skipping",
					"group", cfg.Group, "version", cfg.Version, "kind", cfg.Kind)
				return nil, nil
			}
			return nil, err
		}

		for i := range list.Items {
			if cfg.Name != "" && list.Items[i].GetName() != cfg.Name {
				continue
			}
			matched = append(matched, list.Items[i])
		}
	}

	return matched, nil
}

// TurnCustomResourceDown captures the current value of every configured field and
// overwrites it with the downscale value. Returns true if the resource was skipped.
func TurnCustomResourceDown(
	ctx context.Context,
	c client.Client,
	obj *unstructured.Unstructured,
	cfg *lightsoutv1alpha1.CustomResourceConfig,
	scheduleName string,
) (bool, error) {
	logger := log.FromContext(ctx).WithValues(
		"customResource", obj.GetName(), "namespace", obj.GetNamespace(), "kind", cfg.Kind)

	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	if managedBy, exists := labels[constants.ManagedByLabel]; exists && managedBy != scheduleName {
		logger.Info("skipping custom resource: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	// Already down under this schedule: the captured originals must not be
	// overwritten with the values we ourselves wrote.
	if labels[constants.StateLabel] == constants.StateDown && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping custom resource: already turned down by this schedule")
		return true, nil
	}

	captured := make(map[string]capturedField)

	for _, field := range cfg.SetFields {
		tokens, err := parsePointer(field.Path)
		if err != nil {
			return false, fmt.Errorf("field %q: %w", field.Path, err)
		}

		var value any
		if err := json.Unmarshal(field.Value.Raw, &value); err != nil {
			return false, fmt.Errorf("field %q: decoding value: %w", field.Path, err)
		}
		value = normalizeJSONValue(value)

		paths := expandPointer(obj.Object, tokens)
		if len(paths) == 0 {
			logger.Info("field path matched nothing on custom resource, skipping field", "path", field.Path)
			continue
		}

		for _, path := range paths {
			previous, present := getPointerValue(obj.Object, path)
			captured[formatPointer(path)] = capturedField{Present: present, Value: previous}

			if err := setPointerValue(obj.Object, path, value); err != nil {
				return false, fmt.Errorf("field %q: %w", formatPointer(path), err)
			}
		}
	}

	if len(captured) == 0 {
		logger.Info("no fields matched on custom resource, nothing to turn down")
		return true, nil
	}

	encoded, err := json.Marshal(captured)
	if err != nil {
		return false, fmt.Errorf("encoding captured fields: %w", err)
	}

	// Read metadata after the field loop: a configured field may target a label or
	// annotation, and GetAnnotations/GetLabels return copies, so a snapshot taken
	// earlier would overwrite what was just set.
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[constants.OriginalFieldsAnnotation] = string(encoded)
	delete(annotations, constants.WarmingUpSinceAnnotation)
	obj.SetAnnotations(annotations)

	labels = obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[constants.StateLabel] = constants.StateDown
	labels[constants.ManagedByLabel] = scheduleName
	obj.SetLabels(labels)

	if err := c.Update(ctx, obj); err != nil {
		return false, err
	}

	logger.Info("turned down custom resource", "fields", len(captured))
	return false, nil
}

// RestoreCustomResourceFields writes the captured original values back and moves the
// resource into the warming-up state, where it stays until the workloads it manages
// report ready. Returns true if the resource was skipped.
func RestoreCustomResourceFields(
	ctx context.Context,
	c client.Client,
	obj *unstructured.Unstructured,
	scheduleName string,
	now time.Time,
) (bool, error) {
	logger := log.FromContext(ctx).WithValues(
		"customResource", obj.GetName(), "namespace", obj.GetNamespace(), "kind", obj.GetKind())

	labels := obj.GetLabels()
	if labels[constants.ManagedByLabel] != scheduleName {
		logger.V(1).Info("skipping custom resource: not managed by this schedule")
		return true, nil
	}
	if labels[constants.StateLabel] != constants.StateDown {
		// Already warming up, or in an unexpected state the warmup handler owns.
		return true, nil
	}

	annotations := obj.GetAnnotations()
	encoded := annotations[constants.OriginalFieldsAnnotation]
	if encoded == "" {
		logger.Info("custom resource has no captured fields, clearing lightsout state")
		return true, clearCustomResourceState(ctx, c, obj)
	}

	var captured map[string]capturedField
	if err := json.Unmarshal([]byte(encoded), &captured); err != nil {
		return false, fmt.Errorf("decoding captured fields: %w", err)
	}

	for pointer, field := range captured {
		tokens, err := parsePointer(pointer)
		if err != nil {
			return false, fmt.Errorf("captured field %q: %w", pointer, err)
		}
		if field.Present {
			if err := setPointerValue(obj.Object, tokens, normalizeJSONValue(field.Value)); err != nil {
				return false, fmt.Errorf("restoring %q: %w", pointer, err)
			}
			continue
		}
		if err := deletePointerValue(obj.Object, tokens); err != nil {
			return false, fmt.Errorf("removing %q: %w", pointer, err)
		}
	}

	// Re-read after restoring: a captured field may itself live under metadata, and
	// GetAnnotations/GetLabels return copies, so writing back a snapshot taken before
	// the loop would silently undo what the restore just did.
	annotations = obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	delete(annotations, constants.OriginalFieldsAnnotation)
	annotations[constants.WarmingUpSinceAnnotation] = now.UTC().Format(time.RFC3339)
	obj.SetAnnotations(annotations)

	labels = obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[constants.StateLabel] = constants.StateWarmingUp
	obj.SetLabels(labels)

	if err := c.Update(ctx, obj); err != nil {
		return false, err
	}

	logger.Info("restored custom resource, warming up", "fields", len(captured))
	return false, nil
}

// clearCustomResourceState removes the labels and annotations this schedule added,
// releasing the resource back to its owner.
//
// This patches rather than updates. The object comes from a List and an active
// operator rewrites its own status constantly, so by the time warmup finishes the
// copy in hand is often a version behind and Update is rejected with "the object
// has been modified". A merge patch carries no resourceVersion, so it cannot
// conflict, and a null value removes a key. Only metadata is touched here, which
// makes a patch sufficient: nothing needs the read-modify-write an Update implies.
func clearCustomResourceState(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				constants.StateLabel:     nil,
				constants.ManagedByLabel: nil,
			},
			"annotations": map[string]any{
				constants.OriginalFieldsAnnotation: nil,
				constants.WarmingUpSinceAnnotation: nil,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("building the release patch: %w", err)
	}

	return c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch))
}

// DeleteCustomResources removes matching resources, for operators whose only
// documented way to stop a workload is to delete what they manage and let them
// rebuild it when reconciliation resumes.
func DeleteCustomResources(
	ctx context.Context,
	c client.Client,
	cfg *lightsoutv1alpha1.CustomResourceConfig,
	namespaces []string,
) (int, error) {
	logger := log.FromContext(ctx)

	resources, err := DiscoverCustomResources(ctx, c, cfg, namespaces)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for i := range resources {
		obj := &resources[i]
		if err := c.Delete(ctx, obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return deleted, fmt.Errorf("deleting %s %s/%s: %w", cfg.Kind, obj.GetNamespace(), obj.GetName(), err)
		}
		logger.Info("deleted custom resource", "kind", cfg.Kind, "name", obj.GetName(), "namespace", obj.GetNamespace())
		deleted++
	}

	return deleted, nil
}

// customResourcesDown turns every configured custom resource off, in declaration
// order so that an entry pausing an operator runs before an entry deleting what it
// manages. Errors are logged and reported as events but never block workload scaling.
func customResourcesDown(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	scheduleName string,
	scheduleLabel string,
	namespaces []string,
) {
	logger := log.FromContext(ctx)

	for i := range core.CustomResources {
		cfg := &core.CustomResources[i]

		if cfg.Delete {
			deleted, err := DeleteCustomResources(ctx, c, cfg, namespaces)
			if err != nil {
				logger.Error(err, "failed to delete custom resources", "kind", cfg.Kind)
				emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceDeleteFailed",
					"Failed to delete %s resources: %v", cfg.Kind, err)
				continue
			}
			if deleted > 0 {
				ScalingOperationsTotal.WithLabelValues(scheduleLabel, "", cfg.Kind, constants.OperationDownscale).Add(float64(deleted))
			}
			continue
		}

		resources, err := DiscoverCustomResources(ctx, c, cfg, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover custom resources", "kind", cfg.Kind)
			emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceDiscoveryFailed",
				"Failed to discover %s resources: %v", cfg.Kind, err)
			continue
		}

		for j := range resources {
			obj := &resources[j]
			skipped, err := TurnCustomResourceDown(ctx, c, obj, cfg, scheduleName)
			if err != nil {
				logger.Error(err, "failed to turn down custom resource", "kind", cfg.Kind, "name", obj.GetName())
				ScalingErrorsTotal.WithLabelValues(scheduleLabel, obj.GetNamespace(), cfg.Kind).Inc()
				emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceDownFailed",
					"Failed to turn down %s %s/%s: %v", cfg.Kind, obj.GetNamespace(), obj.GetName(), err)
				continue
			}
			if !skipped {
				ScalingOperationsTotal.WithLabelValues(scheduleLabel, obj.GetNamespace(), cfg.Kind, constants.OperationDownscale).Inc()
			}
		}
	}
}

// customResourcesUp restores every custom resource this schedule turned off and
// moves it into the warming-up state. Deleted resources are left to their operator
// to rebuild once the paused entry is restored.
//
// Returns true if any resource was transitioned on this pass. The caller uses that to
// hold off the warmup check for a cycle: the cached client has not yet caught up with
// the write, so re-reading would hand back a stale object and the completion update
// would lose a conflict against it. A resource restored moments ago cannot be ready
// anyway, so waiting one cycle costs nothing.
func customResourcesUp(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	scheduleName string,
	scheduleLabel string,
	namespaces []string,
	now time.Time,
) bool {
	logger := log.FromContext(ctx)
	transitioned := false

	for i := range core.CustomResources {
		cfg := &core.CustomResources[i]
		if cfg.Delete {
			continue
		}

		resources, err := DiscoverCustomResources(ctx, c, cfg, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover custom resources", "kind", cfg.Kind)
			emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceDiscoveryFailed",
				"Failed to discover %s resources: %v", cfg.Kind, err)
			continue
		}

		for j := range resources {
			obj := &resources[j]
			skipped, err := RestoreCustomResourceFields(ctx, c, obj, scheduleName, now)
			if err != nil {
				logger.Error(err, "failed to restore custom resource", "kind", cfg.Kind, "name", obj.GetName())
				ScalingErrorsTotal.WithLabelValues(scheduleLabel, obj.GetNamespace(), cfg.Kind).Inc()
				emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceUpFailed",
					"Failed to restore %s %s/%s: %v", cfg.Kind, obj.GetNamespace(), obj.GetName(), err)
				continue
			}
			if !skipped {
				transitioned = true
				ScalingOperationsTotal.WithLabelValues(scheduleLabel, obj.GetNamespace(), cfg.Kind, constants.OperationUpscale).Inc()
			}
		}
	}

	return transitioned
}

// handleCustomResourceWarmup holds restored custom resources in the warming-up state
// until the workloads they manage report ready, or the warmup timeout elapses.
//
// Returns true while any resource is still warming up, which keeps application
// workloads scaled down for another cycle so they do not crash-loop against a
// database that has not finished starting.
func handleCustomResourceWarmup(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	scheduleName string,
	namespaces []string,
	now time.Time,
) bool {
	logger := log.FromContext(ctx)

	warmupTimeout := constants.DefaultWarmupTimeout
	if core.CustomResourceWarmupTimeout != nil {
		warmupTimeout = core.CustomResourceWarmupTimeout.Duration
	}

	// Readiness is evaluated per namespace rather than per resource: the scaler has
	// not restored application workloads yet, so every workload still carrying a
	// non-zero replica count in these namespaces belongs to an operator.
	readiness := make(map[string]bool, len(namespaces))

	stillWarmingUp := false

	for i := range core.CustomResources {
		cfg := &core.CustomResources[i]
		if cfg.Delete {
			continue
		}

		resources, err := DiscoverCustomResources(ctx, c, cfg, namespaces)
		if err != nil {
			logger.Error(err, "failed to discover custom resources for warmup", "kind", cfg.Kind)
			emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceDiscoveryFailed",
				"Failed to discover %s resources: %v", cfg.Kind, err)
			continue
		}

		for j := range resources {
			obj := &resources[j]

			labels := obj.GetLabels()
			if labels[constants.ManagedByLabel] != scheduleName {
				continue
			}
			if labels[constants.StateLabel] != constants.StateWarmingUp {
				continue
			}

			warmingUpSince := now
			if ts, ok := obj.GetAnnotations()[constants.WarmingUpSinceAnnotation]; ok {
				if parsed, parseErr := time.Parse(time.RFC3339, ts); parseErr == nil {
					warmingUpSince = parsed
				} else {
					logger.Info("malformed warming-up-since annotation, using current time as fallback",
						"customResource", obj.GetName(), "value", ts)
				}
			}
			timedOut := now.Sub(warmingUpSince) >= warmupTimeout

			ns := obj.GetNamespace()
			ready, checked := readiness[ns]
			if !checked {
				var readErr error
				ready, readErr = CheckWorkloadReadiness(ctx, c, ns)
				if readErr != nil {
					logger.Error(readErr, "failed to check workload readiness, will retry", "namespace", ns)
					stillWarmingUp = true
					continue
				}
				readiness[ns] = ready
			}

			if !ready && !timedOut {
				stillWarmingUp = true
				continue
			}

			if err := clearCustomResourceState(ctx, c, obj); err != nil {
				logger.Error(err, "failed to complete custom resource warmup", "customResource", obj.GetName())
				stillWarmingUp = true
				continue
			}
			if timedOut && !ready {
				logger.Info("warmup timeout elapsed, releasing custom resource",
					"customResource", obj.GetName(), "timeout", warmupTimeout)
				emitCustomResourceWarning(recorder, scheduleObj, "CustomResourceWarmupTimeout",
					"%s %s/%s did not become ready within %s, scaling workloads up anyway",
					obj.GetKind(), obj.GetNamespace(), obj.GetName(), warmupTimeout)
			}
		}
	}

	return stillWarmingUp
}

// restoreAllCustomResources returns every custom resource this schedule owns to its
// original state, ignoring readiness. Used by finalizer cleanup and orphan release,
// where the goal is to leave nothing behind rather than to sequence a startup.
func restoreAllCustomResources(
	ctx context.Context,
	c client.Client,
	core *lightsoutv1alpha1.LightsOutScheduleCore,
	scheduleName string,
	namespaces []string,
	now time.Time,
) []string {
	logger := log.FromContext(ctx)
	var restoreErrors []string

	for i := range core.CustomResources {
		cfg := &core.CustomResources[i]
		if cfg.Delete {
			continue
		}

		resources, err := DiscoverCustomResources(ctx, c, cfg, namespaces)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("list %s: %v", cfg.Kind, err))
			continue
		}

		for j := range resources {
			obj := &resources[j]
			if obj.GetLabels()[constants.ManagedByLabel] != scheduleName {
				continue
			}

			// Put the fields back first, then drop the warming-up state entirely:
			// cleanup has no workloads left to wait for.
			if _, err := RestoreCustomResourceFields(ctx, c, obj, scheduleName, now); err != nil {
				restoreErrors = append(restoreErrors,
					fmt.Sprintf("%s %s/%s: %v", cfg.Kind, obj.GetNamespace(), obj.GetName(), err))
				continue
			}
			if err := clearCustomResourceState(ctx, c, obj); err != nil {
				restoreErrors = append(restoreErrors,
					fmt.Sprintf("%s %s/%s: %v", cfg.Kind, obj.GetNamespace(), obj.GetName(), err))
				continue
			}
			logger.V(1).Info("restored custom resource during cleanup", "kind", cfg.Kind, "name", obj.GetName())
		}
	}

	return restoreErrors
}

func emitCustomResourceWarning(recorder events.EventRecorder, scheduleObj runtime.Object, reason, format string, args ...any) {
	if recorder == nil {
		return
	}
	recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, reason, "CustomResource", format, args...)
}
