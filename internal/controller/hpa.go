package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// hpaScaleUpDisabled is the selectPolicy value that disables HPA scale-up.
const hpaScaleUpDisabled = "Disabled"

var hpaGVK = schema.GroupVersionKind{
	Group:   "autoscaling",
	Version: "v2",
	Kind:    "HorizontalPodAutoscaler",
}

// hpaWatchObject returns an unstructured HPA object suitable for passing to
// controller-runtime's Watches() to register an informer for autoscaling/v2 HPAs.
func hpaWatchObject() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(hpaGVK)
	return obj
}

// listHPAs fetches all HPAs in namespace using the autoscaling/v2 API.
// The HPA informer must be registered (via Watches in SetupWithManager) before
// calling this, so the cache-backed client can serve the List request.
// Returns (nil, nil) if the API is not available on the cluster.
func listHPAs(ctx context.Context, c client.Client, namespace string) (*unstructured.UnstructuredList, error) {
	logger := log.FromContext(ctx)

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   hpaGVK.Group,
		Version: hpaGVK.Version,
		Kind:    hpaGVK.Kind + "List",
	})

	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		if meta.IsNoMatchError(err) {
			logger.V(1).Info("autoscaling/v2 HPA not available on cluster, skipping HPA integration")
			return nil, nil
		}
		return nil, err
	}
	// The Kubernetes API server does not include apiVersion/kind in list items, so each
	// Unstructured item has an empty GVK. Stamp it explicitly so c.Patch can resolve the
	// correct REST endpoint when called later.
	for i := range list.Items {
		list.Items[i].SetGroupVersionKind(hpaGVK)
	}
	return list, nil
}

// findHPA scans a pre-fetched HPA list and returns the entry targeting the given workload
// kind/name. Returns nil if the list is nil or no matching HPA exists.
func findHPA(list *unstructured.UnstructuredList, kind, name string) *unstructured.Unstructured {
	if list == nil {
		return nil
	}
	for i := range list.Items {
		hpa := &list.Items[i]
		targetKind, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "kind")
		targetName, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "name")
		if targetKind == kind && targetName == name {
			return hpa
		}
	}
	return nil
}

// PatchHPAForDownscale finds the HPA targeting the given workload in hpaList, stores the
// original spec.behavior.scaleUp.selectPolicy as an annotation, stamps managed-by, and
// sets spec.behavior.scaleUp.selectPolicy=Disabled to prevent the HPA from fighting back
// when the deployment is scaled to 0.
// hpaList must be fetched with listHPAs before calling, pass nil to skip HPA handling.
// Call this before the workload's replica write on downscale.
// Errors are returned to the caller, the caller treats them as non-fatal.
func PatchHPAForDownscale(ctx context.Context, c client.Client, hpaList *unstructured.UnstructuredList, namespace, kind, name, scheduleName string) error {
	logger := log.FromContext(ctx).WithValues("hpa.namespace", namespace, "hpa.targetKind", kind, "hpa.targetName", name)

	hpa := findHPA(hpaList, kind, name)
	if hpa == nil {
		return nil
	}

	orig := hpa.DeepCopy()

	annotations := hpa.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	managedBy := annotations[constants.ManagedByAnnotation]

	// Skip if owned by a different schedule
	if managedBy != "" && managedBy != scheduleName {
		logger.V(1).Info("skipping HPA: managed by different schedule", "managedBy", managedBy)
		return nil
	}

	// Idempotent: already patched by this schedule
	if managedBy == scheduleName {
		logger.V(1).Info("skipping HPA: already patched by this schedule")
		return nil
	}

	// Read current selectPolicy; if it's already Disabled and there's no managed-by
	// annotation, this is a user-managed HPA - skip it.
	currentPolicy, _, _ := unstructured.NestedString(hpa.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	if currentPolicy == hpaScaleUpDisabled {
		logger.V(1).Info("skipping HPA: user-managed scale-up already disabled")
		return nil
	}

	// Store original policy value (empty string = field was absent, i.e. default behaviour)
	annotations[constants.OriginalHPAScaleUpPolicyAnnotation] = currentPolicy
	annotations[constants.ManagedByAnnotation] = scheduleName
	hpa.SetAnnotations(annotations)

	if err := unstructured.SetNestedField(hpa.Object, hpaScaleUpDisabled, "spec", "behavior", "scaleUp", "selectPolicy"); err != nil {
		return err
	}

	if err := c.Patch(ctx, hpa, client.MergeFrom(orig)); err != nil {
		return err
	}

	logger.Info("disabled HPA scaleUp to prevent fight-back during downscale", "originalSelectPolicy", currentPolicy)
	return nil
}

// RestoreHPA finds the HPA targeting the given workload in hpaList, reads the original
// spec.behavior.scaleUp.selectPolicy from the annotation, restores it, and removes all
// lightsout annotations.
// hpaList must be fetched with listHPAs before calling, pass nil to skip HPA handling.
// Call this after the replica write succeeds on upscale.
// Errors are returned to the caller, the caller treats them as non-fatal.
func RestoreHPA(ctx context.Context, c client.Client, hpaList *unstructured.UnstructuredList, namespace, kind, name, scheduleName string) error {
	logger := log.FromContext(ctx).WithValues("hpa.namespace", namespace, "hpa.targetKind", kind, "hpa.targetName", name)

	hpa := findHPA(hpaList, kind, name)
	if hpa == nil {
		return nil
	}

	orig := hpa.DeepCopy()

	annotations := hpa.GetAnnotations()
	if annotations == nil {
		return nil
	}

	managedBy := annotations[constants.ManagedByAnnotation]

	// Not managed by LightsOut
	if managedBy == "" {
		return nil
	}

	// Managed by a different schedule
	if managedBy != scheduleName {
		logger.V(1).Info("skipping HPA restore: managed by different schedule", "managedBy", managedBy)
		return nil
	}

	origPolicy, hasPolicyAnnotation := annotations[constants.OriginalHPAScaleUpPolicyAnnotation]
	if !hasPolicyAnnotation {
		logger.Info("HPA managed-by annotation present but original-hpa-scale-up-policy absent; skipping restore")
		return nil
	}

	if origPolicy == "" {
		// Field was absent originally - remove selectPolicy so the HPA returns to default
		unstructured.RemoveNestedField(hpa.Object, "spec", "behavior", "scaleUp", "selectPolicy")
	} else {
		if err := unstructured.SetNestedField(hpa.Object, origPolicy, "spec", "behavior", "scaleUp", "selectPolicy"); err != nil {
			return err
		}
	}

	delete(annotations, constants.OriginalHPAScaleUpPolicyAnnotation)
	delete(annotations, constants.ManagedByAnnotation)
	hpa.SetAnnotations(annotations)

	if err := c.Patch(ctx, hpa, client.MergeFrom(orig)); err != nil {
		return err
	}

	logger.Info("restored HPA scaleUp policy", "restoredTo", origPolicy)
	return nil
}
