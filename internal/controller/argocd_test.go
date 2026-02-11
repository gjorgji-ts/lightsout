package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
	"github.com/gjorgji-ts/lightsout/internal/constants"
)

// Helper to create an unstructured ArgoCD Application for tests
func newArgoCDApp(name, destNamespace string, labels map[string]string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})
	app.SetName(name)
	app.SetNamespace("argocd")
	if labels != nil {
		app.SetLabels(labels)
	}
	_ = unstructured.SetNestedField(app.Object, destNamespace, "spec", "destination", "namespace")
	return app
}

func TestDiscoverArgoCDApps(t *testing.T) {
	scheme := runtime.NewScheme()

	argoApp1 := newArgoCDApp("app1", "dev", nil)
	argoApp2 := newArgoCDApp("app2", "staging", nil)
	argoApp3 := newArgoCDApp("app3", "prod", nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(argoApp1, argoApp2, argoApp3).
		Build()

	cfg := &lightsoutv1alpha1.ArgoCDConfig{Namespace: "argocd"}

	tests := []struct {
		name             string
		targetNamespaces []string
		wantCount        int
		wantNames        []string
	}{
		{
			name:             "discover apps matching single namespace",
			targetNamespaces: []string{"dev"},
			wantCount:        1,
			wantNames:        []string{"app1"},
		},
		{
			name:             "discover apps matching multiple namespaces",
			targetNamespaces: []string{"dev", "staging"},
			wantCount:        2,
			wantNames:        []string{"app1", "app2"},
		},
		{
			name:             "no matching namespaces returns empty",
			targetNamespaces: []string{"nonexistent"},
			wantCount:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apps, err := DiscoverArgoCDApps(context.Background(), fakeClient, cfg, tt.targetNamespaces)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(apps) != tt.wantCount {
				t.Errorf("got %d apps, want %d", len(apps), tt.wantCount)
			}
			for _, wantName := range tt.wantNames {
				found := false
				for _, app := range apps {
					if app.GetName() == wantName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected app %q not found in results", wantName)
				}
			}
		})
	}
}

func TestDiscoverArgoCDApps_DefaultNamespace(t *testing.T) {
	scheme := runtime.NewScheme()

	argoApp := newArgoCDApp("app1", "dev", nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(argoApp).
		Build()

	// Empty namespace should default to "argocd"
	cfg := &lightsoutv1alpha1.ArgoCDConfig{}

	apps, err := DiscoverArgoCDApps(context.Background(), fakeClient, cfg, []string{"dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("got %d apps, want 1", len(apps))
	}
}

func TestLabelArgoCDAppDown(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := []struct {
		name          string
		app           *unstructured.Unstructured
		scheduleName  string
		wantSkipped   bool
		wantState     string
		wantManagedBy string
	}{
		{
			name:          "label unlabeled app as down",
			app:           newArgoCDApp("app1", "dev", nil),
			scheduleName:  "dev-schedule",
			wantState:     constants.StateDown,
			wantManagedBy: "dev-schedule",
		},
		{
			name: "skip app already managed by different schedule",
			app: newArgoCDApp("app1", "dev", map[string]string{
				constants.ManagedByLabel: "other-schedule",
				constants.StateLabel:     constants.StateDown,
			}),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "skip app already down by same schedule (idempotent)",
			app: newArgoCDApp("app1", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateDown,
			}),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.app).
				Build()

			skipped, err := LabelArgoCDAppDown(context.Background(), fakeClient, tt.app, tt.scheduleName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}

			if !tt.wantSkipped {
				var updated unstructured.Unstructured
				updated.SetGroupVersionKind(tt.app.GroupVersionKind())
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.app), &updated); err != nil {
					t.Fatalf("failed to get app: %v", err)
				}
				labels := updated.GetLabels()
				if labels[constants.StateLabel] != tt.wantState {
					t.Errorf("state label = %q, want %q", labels[constants.StateLabel], tt.wantState)
				}
				if labels[constants.ManagedByLabel] != tt.wantManagedBy {
					t.Errorf("managed-by label = %q, want %q", labels[constants.ManagedByLabel], tt.wantManagedBy)
				}
			}
		})
	}
}

func TestRemoveArgoCDAppLabels(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := []struct {
		name         string
		app          *unstructured.Unstructured
		scheduleName string
		wantSkipped  bool
	}{
		{
			name: "remove labels from managed app",
			app: newArgoCDApp("app1", "dev", map[string]string{
				constants.ManagedByLabel: "dev-schedule",
				constants.StateLabel:     constants.StateDown,
			}),
			scheduleName: "dev-schedule",
		},
		{
			name:         "skip app without lightsout labels",
			app:          newArgoCDApp("app1", "dev", nil),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
		{
			name: "skip app managed by different schedule",
			app: newArgoCDApp("app1", "dev", map[string]string{
				constants.ManagedByLabel: "other-schedule",
				constants.StateLabel:     constants.StateDown,
			}),
			scheduleName: "dev-schedule",
			wantSkipped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.app).
				Build()

			skipped, err := RemoveArgoCDAppLabels(context.Background(), fakeClient, tt.app, tt.scheduleName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}

			if !tt.wantSkipped {
				var updated unstructured.Unstructured
				updated.SetGroupVersionKind(tt.app.GroupVersionKind())
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.app), &updated); err != nil {
					t.Fatalf("failed to get app: %v", err)
				}
				labels := updated.GetLabels()
				if _, exists := labels[constants.StateLabel]; exists {
					t.Errorf("state label should be removed")
				}
				if _, exists := labels[constants.ManagedByLabel]; exists {
					t.Errorf("managed-by label should be removed")
				}
			}
		})
	}
}
