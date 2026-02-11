package controller

import (
	"context"

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

	// Skip if already labeled down by this schedule (idempotent)
	if labels[constants.StateLabel] == constants.StateDown && labels[constants.ManagedByLabel] == scheduleName {
		logger.V(1).Info("skipping ArgoCD app: already labeled down")
		return true, nil
	}

	labels[constants.StateLabel] = constants.StateDown
	labels[constants.ManagedByLabel] = scheduleName
	app.SetLabels(labels)

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
