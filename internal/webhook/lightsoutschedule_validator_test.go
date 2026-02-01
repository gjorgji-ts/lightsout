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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

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

func TestValidateSchedule_WithRateLimits(t *testing.T) {
	validSchedule := &lightsoutv1alpha1.LightsOutSchedule{
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			Upscale:    "0 6 * * 1-5",
			Downscale:  "0 18 * * 1-5",
			Namespaces: []string{"dev"},
			UpscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize:           ptr(10),
				DelayBetweenBatches: &metav1.Duration{Duration: 5000000000},
			},
			DownscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: ptr(50),
			},
		},
	}

	err := ValidateScheduleSpec(validSchedule)
	if err != nil {
		t.Errorf("ValidateScheduleSpec() unexpected error: %v", err)
	}

	invalidSchedule := &lightsoutv1alpha1.LightsOutSchedule{
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			Upscale:    "0 6 * * 1-5",
			Downscale:  "0 18 * * 1-5",
			Namespaces: []string{"dev"},
			UpscaleRateLimit: &lightsoutv1alpha1.RateLimitConfig{
				BatchSize: ptr(0), // Invalid
			},
		},
	}

	err = ValidateScheduleSpec(invalidSchedule)
	if err == nil {
		t.Error("ValidateScheduleSpec() expected error for invalid batch size")
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
