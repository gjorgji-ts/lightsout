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

package webhook

import (
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

// testScheme returns a *runtime.Scheme with the lightsout API types registered,
// suitable for use with the fake client in webhook tests.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = lightsoutv1alpha1.AddToScheme(s)
	return s
}

func ptr[T any](v T) *T {
	return &v
}

func TestValidateRateLimit(t *testing.T) {
	tests := []struct {
		name      string
		rateLimit *lightsoutv1alpha1.RateLimitConfig
		wantErr   bool
	}{
		{
			name:      "nil rate limit is valid",
			rateLimit: nil,
			wantErr:   false,
		},
		{
			name:      "empty rate limit is valid",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{},
			wantErr:   false,
		},
		{
			name: "valid batch size",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: ptr(10),
			},
			wantErr: false,
		},
		{
			name: "valid batch size and delay",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize:           ptr(10),
				DelayBetweenBatches: &metav1.Duration{Duration: 5000000000}, // 5s
			},
			wantErr: false,
		},
		{
			name: "zero batch size is invalid",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: ptr(0),
			},
			wantErr: true,
		},
		{
			name: "negative batch size is invalid",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: ptr(-1),
			},
			wantErr: true,
		},
		{
			name: "negative delay is invalid",
			rateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize:           ptr(10),
				DelayBetweenBatches: &metav1.Duration{Duration: -1000000000},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateRateLimit(tt.rateLimit, "testField")
			if (len(errs) > 0) != tt.wantErr {
				t.Errorf("ValidateRateLimit() errors = %v, wantErr %v", errs, tt.wantErr)
			}
		})
	}
}

func TestValidateCustomResources(t *testing.T) {
	hibernate := []lightsoutv1alpha1.FieldPatch{{
		Path:  "/metadata/annotations/cnpg.io~1hibernation",
		Value: apiextensionsv1.JSON{Raw: []byte(`"on"`)},
	}}

	tests := []struct {
		name            string
		customResources []lightsoutv1alpha1.CustomResourceConfig
		wantErrs        int
	}{
		{
			name:            "no custom resources is valid",
			customResources: nil,
		},
		{
			name: "patching entry is valid",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster", SetFields: hibernate,
			}},
		},
		{
			name: "deleting entry is valid",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1beta2", Kind: "StrimziPodSet", Delete: true,
			}},
		},
		{
			name: "readyWhen alongside setFields is valid",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "StarRocksCluster", SetFields: hibernate,
				ReadyWhen: []lightsoutv1alpha1.FieldMatch{{
					Path:  "/status/phase",
					Value: apiextensionsv1.JSON{Raw: []byte(`"running"`)},
				}},
			}},
		},
		{
			name: "entry that neither patches nor deletes does nothing",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster",
			}},
			wantErrs: 1,
		},
		{
			name: "delete with setFields and readyWhen is rejected on both",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster", Delete: true, SetFields: hibernate,
				ReadyWhen: []lightsoutv1alpha1.FieldMatch{{
					Path:  "/status/phase",
					Value: apiextensionsv1.JSON{Raw: []byte(`"running"`)},
				}},
			}},
			wantErrs: 2,
		},
		{
			name: "setFields path that is not a JSON Pointer",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster",
				SetFields: []lightsoutv1alpha1.FieldPatch{{
					Path:  "spec/instances",
					Value: apiextensionsv1.JSON{Raw: []byte(`0`)},
				}},
			}},
			wantErrs: 1,
		},
		{
			name: "setFields path with an empty segment",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster",
				SetFields: []lightsoutv1alpha1.FieldPatch{{
					Path:  "/spec//instances",
					Value: apiextensionsv1.JSON{Raw: []byte(`0`)},
				}},
			}},
			wantErrs: 1,
		},
		{
			name: "readyWhen path that is not a JSON Pointer",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster", SetFields: hibernate,
				ReadyWhen: []lightsoutv1alpha1.FieldMatch{{
					Path:  "status/phase",
					Value: apiextensionsv1.JSON{Raw: []byte(`"running"`)},
				}},
			}},
			wantErrs: 1,
		},
		{
			name: "missing values are rejected",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{{
				Version: "v1", Kind: "Cluster",
				SetFields: []lightsoutv1alpha1.FieldPatch{{Path: "/spec/instances"}},
				ReadyWhen: []lightsoutv1alpha1.FieldMatch{{Path: "/status/phase"}},
			}},
			wantErrs: 2,
		},
		{
			name: "each entry is reported separately",
			customResources: []lightsoutv1alpha1.CustomResourceConfig{
				{Version: "v1", Kind: "Cluster"},
				{Version: "v1", Kind: "Kafka"},
			},
			wantErrs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateCustomResources(tt.customResources)
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors, want %d: %v", len(errs), tt.wantErrs, errs)
			}
		})
	}
}

func TestValidateScheduleCore_CustomResourceWarmupTimeout(t *testing.T) {
	core := func(timeout time.Duration) *lightsoutv1alpha1.LightsOutScheduleCore {
		return &lightsoutv1alpha1.LightsOutScheduleCore{
			Upscale:                     "0 6 * * 1-5",
			Downscale:                   "0 18 * * 1-5",
			CustomResourceWarmupTimeout: &metav1.Duration{Duration: timeout},
		}
	}

	if err := ValidateScheduleCore(core(5 * time.Minute)); err != nil {
		t.Errorf("positive warmup timeout should be valid, got %v", err)
	}
	if err := ValidateScheduleCore(core(0)); err == nil {
		t.Error("zero warmup timeout should be rejected: the gate would never hold")
	}
	if err := ValidateScheduleCore(core(-time.Minute)); err == nil {
		t.Error("negative warmup timeout should be rejected")
	}
}

func TestValidateSchedule_WithRateLimits(t *testing.T) {
	validSchedule := &lightsoutv1alpha1.LightsOutSchedule{
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * 1-5",
				Downscale: "0 18 * * 1-5",
				UpscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
					BatchSize:           ptr(10),
					DelayBetweenBatches: &metav1.Duration{Duration: 5000000000},
				},
				DownscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
					BatchSize: ptr(50),
				},
			},
			Namespaces: []string{"dev"},
		},
	}

	err := ValidateScheduleSpec(validSchedule)
	if err != nil {
		t.Errorf("ValidateScheduleSpec() unexpected error: %v", err)
	}

	invalidSchedule := &lightsoutv1alpha1.LightsOutSchedule{
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * 1-5",
				Downscale: "0 18 * * 1-5",
				UpscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
					BatchSize: ptr(0), // Invalid
				},
			},
			Namespaces: []string{"dev"},
		},
	}

	err = ValidateScheduleSpec(invalidSchedule)
	if err == nil {
		t.Error("ValidateScheduleSpec() expected error for invalid batch size")
	}
}

func TestValidateScheduleSpec_ArgoCDConfig(t *testing.T) {
	validBase := lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
			Namespaces: []string{"dev"},
		},
	}

	tests := []struct {
		name    string
		argoCD  *lightsoutv1alpha1.ArgoCDConfig
		wantErr bool
	}{
		{
			name:    "nil argoCD config is valid",
			argoCD:  nil,
			wantErr: false,
		},
		{
			name:    "empty namespace defaults to argocd (valid)",
			argoCD:  &lightsoutv1alpha1.ArgoCDConfig{},
			wantErr: false,
		},
		{
			name:    "valid custom namespace",
			argoCD:  &lightsoutv1alpha1.ArgoCDConfig{Namespace: "argocd-system"},
			wantErr: false,
		},
		{
			name:    "invalid namespace name",
			argoCD:  &lightsoutv1alpha1.ArgoCDConfig{Namespace: "INVALID_NS!"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule := validBase.DeepCopy()
			schedule.Spec.ArgoCD = tt.argoCD
			err := ValidateScheduleSpec(schedule)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateScheduleSpec() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchedulesOverlap(t *testing.T) {
	tests := []struct {
		name    string
		a       *lightsoutv1alpha1.LightsOutSchedule
		b       *lightsoutv1alpha1.LightsOutSchedule
		overlap bool
	}{
		{
			name: "no overlap - different explicit namespaces",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"dev", "staging"},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"prod", "test"},
				},
			},
			overlap: false,
		},
		{
			name: "overlap - same explicit namespace",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"dev", "staging"},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"staging", "prod"},
				},
			},
			overlap: true,
		},
		{
			name: "overlap - identical namespace selectors",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "dev"},
					},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "dev"},
					},
				},
			},
			overlap: true,
		},
		{
			name: "overlap - both have empty selectors (match all)",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{},
				},
			},
			overlap: true,
		},
		{
			name: "overlap - one empty selector with explicit namespaces",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"dev"},
				},
			},
			overlap: true,
		},
		{
			name: "overlap - different non-empty selectors (conservative)",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "dev"},
					},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"team": "alpha"},
					},
				},
			},
			overlap: true, // Conservative - can't evaluate without listing namespaces
		},
		{
			name: "no overlap - one selector, one explicit, no match",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "dev"},
					},
				},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
					Namespaces: []string{"prod"},
				},
			},
			overlap: false, // Selector doesn't match all, explicit doesn't intersect
		},
		{
			name: "no overlap - both empty",
			a: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{},
			},
			b: &lightsoutv1alpha1.LightsOutSchedule{
				Spec: lightsoutv1alpha1.LightsOutScheduleSpec{},
			},
			overlap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SchedulesOverlap(tt.a, tt.b)
			if result != tt.overlap {
				t.Errorf("SchedulesOverlap() = %v, want %v", result, tt.overlap)
			}
			// Also test reverse order (symmetry)
			resultReverse := SchedulesOverlap(tt.b, tt.a)
			if resultReverse != tt.overlap {
				t.Errorf("SchedulesOverlap() reverse = %v, want %v", resultReverse, tt.overlap)
			}
		})
	}
}

func TestLabelSelectorsEqual(t *testing.T) {
	tests := []struct {
		name  string
		a     *metav1.LabelSelector
		b     *metav1.LabelSelector
		equal bool
	}{
		{
			name:  "both nil",
			a:     nil,
			b:     nil,
			equal: true,
		},
		{
			name:  "one nil",
			a:     nil,
			b:     &metav1.LabelSelector{},
			equal: false,
		},
		{
			name:  "both empty",
			a:     &metav1.LabelSelector{},
			b:     &metav1.LabelSelector{},
			equal: true,
		},
		{
			name: "same match labels",
			a: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev", "team": "alpha"},
			},
			b: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev", "team": "alpha"},
			},
			equal: true,
		},
		{
			name: "different match labels",
			a: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev"},
			},
			b: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "prod"},
			},
			equal: false,
		},
		{
			name: "different number of labels",
			a: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev"},
			},
			b: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev", "team": "alpha"},
			},
			equal: false,
		},
		{
			name: "same match expressions",
			a: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev", "staging"}},
				},
			},
			b: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev", "staging"}},
				},
			},
			equal: true,
		},
		{
			name: "different match expressions",
			a: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev"}},
				},
			},
			b: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prod"}},
				},
			},
			equal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LabelSelectorsEqual(tt.a, tt.b)
			if result != tt.equal {
				t.Errorf("LabelSelectorsEqual() = %v, want %v", result, tt.equal)
			}
		})
	}
}

func TestIsEmptyLabelSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector *metav1.LabelSelector
		isEmpty  bool
	}{
		{
			name:     "nil is empty",
			selector: nil,
			isEmpty:  true,
		},
		{
			name:     "empty selector is empty",
			selector: &metav1.LabelSelector{},
			isEmpty:  true,
		},
		{
			name: "selector with match labels is not empty",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "dev"},
			},
			isEmpty: false,
		},
		{
			name: "selector with match expressions is not empty",
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "env", Operator: metav1.LabelSelectorOpExists},
				},
			},
			isEmpty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsEmptyLabelSelector(tt.selector)
			if result != tt.isEmpty {
				t.Errorf("IsEmptyLabelSelector() = %v, want %v", result, tt.isEmpty)
			}
		})
	}
}
