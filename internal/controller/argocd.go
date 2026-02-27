package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
