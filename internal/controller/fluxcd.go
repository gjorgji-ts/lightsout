package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

var fluxKustomizationGVK = schema.GroupVersionKind{
	Group:   "kustomize.toolkit.fluxcd.io",
	Version: "v1",
	Kind:    "Kustomization",
}

var fluxHelmReleaseGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmRelease",
}

func DiscoverFluxResources(
	ctx context.Context,
	c client.Client,
	cfg *lightsoutv1alpha1.FluxCDConfig,
	targetNamespaces []string,
) ([]unstructured.Unstructured, error) {
	logger := log.FromContext(ctx)

	fluxNS := cfg.Namespace
	if fluxNS == "" {
		fluxNS = constants.DefaultFluxCDNamespace
	}

	targetSet := make(map[string]struct{}, len(targetNamespaces))
	for _, ns := range targetNamespaces {
		targetSet[ns] = struct{}{}
	}

	seen := make(map[string]struct{})
	var matched []unstructured.Unstructured

	for _, gvk := range []schema.GroupVersionKind{fluxKustomizationGVK, fluxHelmReleaseGVK} {
		listGVK := schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"}

		// Location 1: cluster-wide match by spec.targetNamespace.
		// Searching cluster-wide (not just fluxNS) covers multi-tenant setups where
		// HelmReleases/Kustomizations live in team namespaces rather than flux-system.
		fluxList := &unstructured.UnstructuredList{}
		fluxList.SetGroupVersionKind(listGVK)
		if err := c.List(ctx, fluxList); err != nil {
			if meta.IsNoMatchError(err) {
				logger.Info("FluxCD CRD not found on cluster, skipping FluxCD integration", "kind", gvk.Kind)
				return nil, nil
			}
			return nil, err
		}
		for _, item := range fluxList.Items {
			targetNS, _, _ := unstructured.NestedString(item.Object, "spec", "targetNamespace")
			if targetNS == "" {
				continue
			}
			if _, ok := targetSet[targetNS]; !ok {
				continue
			}
			key := item.GetNamespace() + "/" + item.GetName()
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				matched = append(matched, item)
			}
		}

		// Location 2: each target namespace resources with no spec.targetNamespace
		// (co-located pattern: HelmRelease lives in the same namespace it deploys to).
		// Skip fluxNS itself: a resource in fluxNS with no spec.targetNamespace is out of scope.
		for _, ns := range targetNamespaces {
			if ns == fluxNS {
				continue
			}
			nsList := &unstructured.UnstructuredList{}
			nsList.SetGroupVersionKind(listGVK)
			if err := c.List(ctx, nsList, client.InNamespace(ns)); err != nil {
				if meta.IsNoMatchError(err) {
					logger.Info("FluxCD CRD not found on cluster, skipping FluxCD integration", "kind", gvk.Kind)
					return nil, nil
				}
				return nil, err
			}
			for _, item := range nsList.Items {
				targetNS, _, _ := unstructured.NestedString(item.Object, "spec", "targetNamespace")
				if targetNS != "" {
					continue
				}
				key := item.GetNamespace() + "/" + item.GetName()
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					matched = append(matched, item)
				}
			}
		}
	}

	logger.V(1).Info("discovered Flux resources", "matched", len(matched))
	return matched, nil
}

func SuspendFluxResource(
	ctx context.Context,
	c client.Client,
	obj *unstructured.Unstructured,
	scheduleName string,
) (bool, error) {
	logger := log.FromContext(ctx).WithValues("flux-resource", obj.GetName(), "namespace", obj.GetNamespace())

	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Skip if managed by different schedule
	if managedBy, exists := labels[constants.ManagedByLabel]; exists && managedBy != scheduleName {
		logger.Info("skipping Flux resource: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend")

	// Skip if already suspended by user (no managed-by label)
	if suspended && labels[constants.ManagedByLabel] == "" {
		logger.V(1).Info("skipping Flux resource: already suspended by user")
		return true, nil
	}

	// Skip if already suspended down by this schedule (idempotent)
	if suspended && labels[constants.StateLabel] == constants.StateDown && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping Flux resource: already suspended by this schedule")
		return true, nil
	}

	// Clear warming-up annotation if transitioning back to down mid-warmup
	annotations := obj.GetAnnotations()
	if annotations != nil {
		delete(annotations, constants.WarmingUpSinceAnnotation)
		obj.SetAnnotations(annotations)
	}

	labels[constants.StateLabel] = constants.StateDown
	labels[constants.ManagedByLabel] = scheduleName
	obj.SetLabels(labels)

	if err := unstructured.SetNestedField(obj.Object, true, "spec", "suspend"); err != nil {
		return false, err
	}

	if err := c.Update(ctx, obj); err != nil {
		return false, err
	}

	logger.Info("suspended Flux resource")
	return false, nil
}

func TransitionFluxResourceToWarmingUp(
	ctx context.Context,
	c client.Client,
	obj *unstructured.Unstructured,
	scheduleName string,
	now time.Time,
) (bool, error) {
	logger := log.FromContext(ctx).WithValues("flux-resource", obj.GetName(), "namespace", obj.GetNamespace())

	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	if managedBy, exists := labels[constants.ManagedByLabel]; exists && managedBy != scheduleName {
		logger.Info("skipping Flux resource: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	if labels[constants.StateLabel] == constants.StateWarmingUp && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping Flux resource: already in warming-up state")
		return true, nil
	}

	labels[constants.StateLabel] = constants.StateWarmingUp
	labels[constants.ManagedByLabel] = scheduleName
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[constants.WarmingUpSinceAnnotation] = now.UTC().Format(time.RFC3339)
	obj.SetAnnotations(annotations)

	if err := c.Update(ctx, obj); err != nil {
		return false, err
	}

	logger.Info("transitioned Flux resource to warming-up")
	return false, nil
}

func CompleteFluxWarmup(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	logger := log.FromContext(ctx).WithValues("flux-resource", obj.GetName(), "namespace", obj.GetNamespace())

	labels := obj.GetLabels()
	if labels != nil {
		delete(labels, constants.StateLabel)
		delete(labels, constants.ManagedByLabel)
		obj.SetLabels(labels)
	}

	annotations := obj.GetAnnotations()
	if annotations != nil {
		delete(annotations, constants.WarmingUpSinceAnnotation)
		obj.SetAnnotations(annotations)
	}

	if err := unstructured.SetNestedField(obj.Object, false, "spec", "suspend"); err != nil {
		return err
	}

	if err := c.Update(ctx, obj); err != nil {
		return err
	}

	logger.Info("completed warming-up, resumed Flux resource")
	return nil
}

// ResumeFluxResource removes lightsout labels/annotations and sets spec.suspend=false.
// Used during finalizer cleanup. Returns true if skipped (not managed by this schedule).
func ResumeFluxResource(
	ctx context.Context,
	c client.Client,
	obj *unstructured.Unstructured,
	scheduleName string,
) (bool, error) {
	logger := log.FromContext(ctx).WithValues("flux-resource", obj.GetName(), "namespace", obj.GetNamespace())

	labels := obj.GetLabels()
	managedBy, exists := labels[constants.ManagedByLabel]
	if !exists {
		logger.V(1).Info("skipping Flux resource: not managed by lightsout")
		return true, nil
	}
	if managedBy != scheduleName {
		logger.Info("skipping Flux resource: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	delete(labels, constants.StateLabel)
	delete(labels, constants.ManagedByLabel)
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations != nil {
		delete(annotations, constants.WarmingUpSinceAnnotation)
		obj.SetAnnotations(annotations)
	}

	if err := unstructured.SetNestedField(obj.Object, false, "spec", "suspend"); err != nil {
		return false, err
	}

	if err := c.Update(ctx, obj); err != nil {
		return false, err
	}

	logger.Info("resumed Flux resource during cleanup")
	return false, nil
}

// labelFluxResourcesDown discovers and suspends Flux resources in target namespaces.
// Errors are logged but do not block workload scaling.
func labelFluxResourcesDown(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	fluxCDSpec *lightsoutv1alpha1.FluxCDConfig,
	scheduleName string,
	namespaces []string,
) {
	logger := log.FromContext(ctx)

	resources, err := DiscoverFluxResources(ctx, c, fluxCDSpec, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover Flux resources, continuing with workload scaling")
		if recorder != nil {
			recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, "FluxCDDiscoveryFailed", "FluxCD",
				"Failed to discover Flux resources: %v", err)
		}
		return
	}

	for i := range resources {
		if _, err := SuspendFluxResource(ctx, c, &resources[i], scheduleName); err != nil {
			logger.Error(err, "failed to suspend Flux resource", "resource", resources[i].GetName())
		}
	}
}

// handleFluxCDWarmup drives the warming-up state machine for all Flux resources matched
// by the schedule during the Up period.
// Returns true if any resource is still warming up and the reconciler should requeue.
func handleFluxCDWarmup(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	fluxCDSpec *lightsoutv1alpha1.FluxCDConfig,
	scheduleName string,
	namespaces []string,
	now time.Time,
) bool {
	logger := log.FromContext(ctx)

	resources, err := DiscoverFluxResources(ctx, c, fluxCDSpec, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover Flux resources for warmup handling")
		if recorder != nil {
			recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, "FluxCDDiscoveryFailed", "FluxCD",
				"Failed to discover Flux resources: %v", err)
		}
		return false
	}

	warmupTimeout := constants.DefaultWarmupTimeout
	if fluxCDSpec.WarmupTimeout != nil {
		warmupTimeout = fluxCDSpec.WarmupTimeout.Duration
	}

	stillWarmingUp := false

	for i := range resources {
		obj := &resources[i]
		state := obj.GetLabels()[constants.StateLabel]

		switch state {
		case constants.StateDown:
			if _, err := TransitionFluxResourceToWarmingUp(ctx, c, obj, scheduleName, now); err != nil {
				logger.Error(err, "failed to transition Flux resource to warming-up", "resource", obj.GetName())
			}
			stillWarmingUp = true

		case constants.StateWarmingUp:
			warmingUpSince := now
			annotations := obj.GetAnnotations()
			if ts, ok := annotations[constants.WarmingUpSinceAnnotation]; ok {
				if parsed, parseErr := time.Parse(time.RFC3339, ts); parseErr == nil {
					warmingUpSince = parsed
				} else {
					logger.Info("malformed warming-up-since annotation, using current time as fallback",
						"resource", obj.GetName(), "value", ts)
				}
			}

			timedOut := now.Sub(warmingUpSince) >= warmupTimeout

			// Determine namespace to check: spec.targetNamespace if set, else own namespace.
			targetNS, _, _ := unstructured.NestedString(obj.Object, "spec", "targetNamespace")
			if targetNS == "" {
				targetNS = obj.GetNamespace()
			}

			ready := timedOut
			if !ready {
				var readErr error
				ready, readErr = CheckWorkloadReadiness(ctx, c, targetNS)
				if readErr != nil {
					logger.Error(readErr, "failed to check workload readiness, will retry",
						"resource", obj.GetName(), "namespace", targetNS)
					stillWarmingUp = true
					continue
				}
			}

			if ready {
				if err := CompleteFluxWarmup(ctx, c, obj); err != nil {
					logger.Error(err, "failed to complete Flux warmup", "resource", obj.GetName())
					stillWarmingUp = true
				} else if timedOut {
					logger.Info("warmup timeout elapsed, Flux resource resumed",
						"resource", obj.GetName(), "timeout", warmupTimeout)
				}
			} else {
				stillWarmingUp = true
			}

		default:
			// Resource has managed-by label but an unrecognised state transition to
			// warming-up so it is not silently abandoned in a limbo state.
			if _, err := TransitionFluxResourceToWarmingUp(ctx, c, obj, scheduleName, now); err != nil {
				logger.Error(err, "failed to transition Flux resource to warming-up from unknown state", "resource", obj.GetName())
			}
			stillWarmingUp = true
		}
	}

	return stillWarmingUp
}
