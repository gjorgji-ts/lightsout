package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

var argoCDAppGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "Application",
}

// DiscoverArgoCDApps lists ArgoCD Application CRDs from the configured namespace
// and returns those whose spec.destination.namespace matches any of the target namespaces.
func DiscoverArgoCDApps(
	ctx context.Context,
	c client.Client,
	cfg *lightsoutv1alpha1.ArgoCDConfig,
	targetNamespaces []string,
) ([]unstructured.Unstructured, error) {
	logger := log.FromContext(ctx)

	ns := cfg.Namespace
	if ns == "" {
		ns = constants.DefaultArgoCDNamespace
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   argoCDAppGVK.Group,
		Version: argoCDAppGVK.Version,
		Kind:    argoCDAppGVK.Kind + "List",
	})

	if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
		if meta.IsNoMatchError(err) {
			logger.Info("ArgoCD Application CRD not found on cluster, skipping ArgoCD integration")
			return nil, nil
		}
		return nil, err
	}

	// Build lookup set for target namespaces
	targetSet := make(map[string]struct{}, len(targetNamespaces))
	for _, n := range targetNamespaces {
		targetSet[n] = struct{}{}
	}

	var matched []unstructured.Unstructured
	for _, app := range list.Items {
		destNS, found, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")
		if !found {
			continue
		}
		if _, ok := targetSet[destNS]; ok {
			matched = append(matched, app)
		}
	}

	logger.V(1).Info("discovered ArgoCD apps", "total", len(list.Items), "matched", len(matched))
	return matched, nil
}

// LabelArgoCDAppDown adds lightsout labels to an ArgoCD Application to signal downscale state.
// Returns true if the operation was skipped (already labeled or managed by different schedule).
func LabelArgoCDAppDown(ctx context.Context, c client.Client, app *unstructured.Unstructured, scheduleName string) (bool, error) {
	logger := log.FromContext(ctx).WithValues("argocd-app", app.GetName())

	labels := app.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Skip if managed by different schedule
	if managedBy, exists := labels[constants.ManagedByLabel]; exists && managedBy != scheduleName {
		logger.Info("skipping ArgoCD app: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	// Skip if already labeled down by this schedule
	if labels[constants.StateLabel] == constants.StateDown && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping ArgoCD app: already labeled down")
		return true, nil
	}

	labels[constants.StateLabel] = constants.StateDown
	labels[constants.ManagedByLabel] = scheduleName
	app.SetLabels(labels)

	// Clean up any warming-up-since annotation left from a previous upscale cycle
	// (edge case: downscale fires while the app is still in warming-up state)
	annotations := app.GetAnnotations()
	if annotations != nil {
		delete(annotations, constants.WarmingUpSinceAnnotation)
		app.SetAnnotations(annotations)
	}

	if err := c.Update(ctx, app); err != nil {
		return false, err
	}

	logger.Info("labeled ArgoCD app as down")
	return false, nil
}

// RemoveArgoCDAppLabels removes lightsout labels from an ArgoCD Application on upscale.
// Returns true if the operation was skipped (not managed or managed by different schedule).
func RemoveArgoCDAppLabels(ctx context.Context, c client.Client, app *unstructured.Unstructured, scheduleName string) (bool, error) {
	logger := log.FromContext(ctx).WithValues("argocd-app", app.GetName())

	labels := app.GetLabels()

	// Skip if no managed-by label (not managed by lightsout)
	managedBy, exists := labels[constants.ManagedByLabel]
	if !exists {
		logger.V(1).Info("skipping ArgoCD app: not managed by lightsout")
		return true, nil
	}

	// Skip if managed by different schedule
	if managedBy != scheduleName {
		logger.Info("skipping ArgoCD app: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	delete(labels, constants.StateLabel)
	delete(labels, constants.ManagedByLabel)
	app.SetLabels(labels)

	if err := c.Update(ctx, app); err != nil {
		return false, err
	}

	logger.Info("removed lightsout labels from ArgoCD app")
	return false, nil
}

// LabelArgoCDAppWarmingUp transitions an ArgoCD Application from the down state to the
// warming-up state. It sets state=warming-up and records the current time in the
// warming-up-since annotation so the timeout can be enforced on subsequent reconciles.
// Returns true if the operation was skipped.
func LabelArgoCDAppWarmingUp(ctx context.Context, c client.Client, app *unstructured.Unstructured, scheduleName string, now time.Time) (bool, error) {
	logger := log.FromContext(ctx).WithValues("argocd-app", app.GetName())

	labels := app.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Skip if managed by different schedule
	if managedBy, exists := labels[constants.ManagedByLabel]; exists && managedBy != scheduleName {
		logger.Info("skipping ArgoCD app: managed by different schedule", "managedBy", managedBy)
		return true, nil
	}

	// Skip if already in warming-up state
	if labels[constants.StateLabel] == constants.StateWarmingUp && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping ArgoCD app: already in warming-up state")
		return true, nil
	}

	labels[constants.StateLabel] = constants.StateWarmingUp
	labels[constants.ManagedByLabel] = scheduleName
	app.SetLabels(labels)

	annotations := app.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[constants.WarmingUpSinceAnnotation] = now.UTC().Format(time.RFC3339)
	app.SetAnnotations(annotations)

	if err := c.Update(ctx, app); err != nil {
		return false, err
	}

	logger.Info("labeled ArgoCD app as warming-up")
	return false, nil
}

// CheckWorkloadReadiness reports whether all active Deployments and StatefulSets
// in the given namespace have all their desired replicas ready.
// Workloads with spec.replicas == 0 are skipped (intentionally at zero).
// All workloads in the namespace are checked so that the warming-up signal
// accurately reflects the full health of the namespace, not just the subset
// lightsout scaled up.
func CheckWorkloadReadiness(ctx context.Context, c client.Client, namespace string) (bool, error) {
	var deployments appsv1.DeploymentList
	if err := c.List(ctx, &deployments, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for _, d := range deployments.Items {
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if desired == 0 {
			continue
		}
		if d.Status.ReadyReplicas < desired {
			return false, nil
		}
	}

	var statefulsets appsv1.StatefulSetList
	if err := c.List(ctx, &statefulsets, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for _, s := range statefulsets.Items {
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		if desired == 0 {
			continue
		}
		if s.Status.ReadyReplicas < desired {
			return false, nil
		}
	}

	return true, nil
}

// labelArgoCDAppsDown discovers and labels ArgoCD apps in namespaces as down.
// Errors are logged but do not block workload scaling.
func labelArgoCDAppsDown(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	argoCDSpec *lightsoutv1alpha1.ArgoCDConfig,
	scheduleName string,
	namespaces []string,
) {
	logger := log.FromContext(ctx)

	apps, err := DiscoverArgoCDApps(ctx, c, argoCDSpec, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover ArgoCD apps, continuing with workload scaling")
		if recorder != nil {
			recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, "ArgoCDDiscoveryFailed", "ArgoCD",
				"Failed to discover ArgoCD apps: %v", err)
		}
		return
	}

	for i := range apps {
		if _, err := LabelArgoCDAppDown(ctx, c, &apps[i], scheduleName); err != nil {
			logger.Error(err, "failed to label ArgoCD app", "app", apps[i].GetName())
		}
	}
}

// handleArgoCDWarmup drives the warming-up state machine for all ArgoCD apps matched
// by the schedule during the Up period.
// Returns true if any app is still in the warming-up state and the reconciler should
// requeue at WarmupCheckInterval.
// Errors are logged but do not block reconciliation.
func handleArgoCDWarmup(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	scheduleObj runtime.Object,
	argoCDSpec *lightsoutv1alpha1.ArgoCDConfig,
	scheduleName string,
	namespaces []string,
	now time.Time,
) bool {
	logger := log.FromContext(ctx)

	apps, err := DiscoverArgoCDApps(ctx, c, argoCDSpec, namespaces)
	if err != nil {
		logger.Error(err, "failed to discover ArgoCD apps for warmup handling")
		if recorder != nil {
			recorder.Eventf(scheduleObj, nil, corev1.EventTypeWarning, "ArgoCDDiscoveryFailed", "ArgoCD",
				"Failed to discover ArgoCD apps: %v", err)
		}
		return false
	}

	warmupTimeout := constants.DefaultWarmupTimeout
	if argoCDSpec.WarmupTimeout != nil {
		warmupTimeout = argoCDSpec.WarmupTimeout.Duration
	}

	stillWarmingUp := false

	for i := range apps {
		app := &apps[i]
		labels := app.GetLabels()
		state := labels[constants.StateLabel]

		switch state {
		case constants.StateDown:
			// Workloads just scaled up — transition to warming-up
			if _, err := LabelArgoCDAppWarmingUp(ctx, c, app, scheduleName, now); err != nil {
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
				ready, readErr = CheckWorkloadReadiness(ctx, c, destNS)
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
				if err := CompleteArgoCDWarmup(ctx, c, app); err != nil {
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

// CompleteArgoCDWarmup removes the lightsout state labels and the warming-up-since
// annotation from an ArgoCD Application. Workload labels are already cleaned up by
// the scaler at upscale time, so only the ArgoCD app needs updating here.
func CompleteArgoCDWarmup(ctx context.Context, c client.Client, app *unstructured.Unstructured) error {
	logger := log.FromContext(ctx).WithValues("argocd-app", app.GetName())

	labels := app.GetLabels()
	if labels != nil {
		delete(labels, constants.StateLabel)
		delete(labels, constants.ManagedByLabel)
		app.SetLabels(labels)
	}

	annotations := app.GetAnnotations()
	if annotations != nil {
		delete(annotations, constants.WarmingUpSinceAnnotation)
		app.SetAnnotations(annotations)
	}

	if err := c.Update(ctx, app); err != nil {
		return err
	}
	logger.Info("completed warming-up, removed labels from ArgoCD app")
	return nil
}
