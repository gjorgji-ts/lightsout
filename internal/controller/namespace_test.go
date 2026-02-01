package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

func TestDiscoverNamespaces(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lightsoutv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name       string
		namespaces []corev1.Namespace
		spec       lightsoutv1alpha1.LightsOutScheduleSpec
		want       []string
		wantErr    bool
	}{
		{
			name: "selector matches namespaces",
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-frontend", Labels: map[string]string{"environment": "dev"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-backend", Labels: map[string]string{"environment": "dev"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "prod-frontend", Labels: map[string]string{"environment": "prod"}}},
			},
			spec: lightsoutv1alpha1.LightsOutScheduleSpec{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"environment": "dev"},
				},
			},
			want: []string{"dev-backend", "dev-frontend"},
		},
		{
			name: "explicit namespace list",
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "team-alpha"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "team-beta"}},
			},
			spec: lightsoutv1alpha1.LightsOutScheduleSpec{
				Namespaces: []string{"team-alpha", "team-beta"},
			},
			want: []string{"team-alpha", "team-beta"},
		},
		{
			name: "union of selector and explicit list",
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-frontend", Labels: map[string]string{"environment": "dev"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "team-special"}},
			},
			spec: lightsoutv1alpha1.LightsOutScheduleSpec{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"environment": "dev"},
				},
				Namespaces: []string{"team-special"},
			},
			want: []string{"dev-frontend", "team-special"},
		},
		{
			name: "exclusions applied",
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-frontend", Labels: map[string]string{"environment": "dev"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-critical", Labels: map[string]string{"environment": "dev"}}},
			},
			spec: lightsoutv1alpha1.LightsOutScheduleSpec{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"environment": "dev"},
				},
				ExcludeNamespaces: []string{"dev-critical"},
			},
			want: []string{"dev-frontend"},
		},
		{
			name: "system namespaces excluded automatically",
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-app", Labels: map[string]string{"all": "true"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", Labels: map[string]string{"all": "true"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "kube-public", Labels: map[string]string{"all": "true"}}},
			},
			spec: lightsoutv1alpha1.LightsOutScheduleSpec{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"all": "true"},
				},
			},
			want: []string{"dev-app"},
		},
		{
			name:       "no selection criteria returns empty",
			namespaces: []corev1.Namespace{},
			spec:       lightsoutv1alpha1.LightsOutScheduleSpec{},
			want:       []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]client.Object, 0, len(tt.namespaces))
			for i := range tt.namespaces {
				objects = append(objects, &tt.namespaces[i])
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			got, err := DiscoverNamespaces(context.Background(), fakeClient, &tt.spec)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("got %v namespaces, want %v", len(got), len(tt.want))
				return
			}

			for i, ns := range got {
				if ns != tt.want[i] {
					t.Errorf("namespace[%d] = %v, want %v", i, ns, tt.want[i])
				}
			}
		})
	}
}
