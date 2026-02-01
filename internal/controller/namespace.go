package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

// SystemNamespaces that should always be excluded
var SystemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// DiscoverNamespaces returns the list of namespaces to manage based on the schedule spec
func DiscoverNamespaces(ctx context.Context, c client.Client, spec *lightsoutv1alpha1.LightsOutScheduleSpec) ([]string, error) {
	namespaceSet := make(map[string]bool)

	// Add namespaces from label selector
	if spec.NamespaceSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(spec.NamespaceSelector)
		if err != nil {
			return nil, err
		}

		var namespaceList corev1.NamespaceList
		if err := c.List(ctx, &namespaceList, &client.ListOptions{
			LabelSelector: selector,
		}); err != nil {
			return nil, err
		}

		for _, ns := range namespaceList.Items {
			namespaceSet[ns.Name] = true
		}
	}

	// Add explicit namespaces (UNION behavior)
	for _, ns := range spec.Namespaces {
		namespaceSet[ns] = true
	}

	// Remove excluded namespaces
	for _, ns := range spec.ExcludeNamespaces {
		delete(namespaceSet, ns)
	}

	// Remove system namespaces
	for ns := range SystemNamespaces {
		delete(namespaceSet, ns)
	}

	// Convert to sorted slice
	result := make([]string, 0, len(namespaceSet))
	for ns := range namespaceSet {
		result = append(result, ns)
	}
	sort.Strings(result)

	return result, nil
}

// ShouldExcludeWorkload checks if a workload should be excluded based on labels
func ShouldExcludeWorkload(workloadLabels map[string]string, excludeSelector *metav1.LabelSelector) (bool, error) {
	if excludeSelector == nil {
		return false, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(excludeSelector)
	if err != nil {
		return false, err
	}

	return selector.Matches(labels.Set(workloadLabels)), nil
}
