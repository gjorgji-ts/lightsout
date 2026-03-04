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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

func TestNamespaceScheduleValidator_ValidCron(t *testing.T) {
	v := &LightsOutNamespaceScheduleValidator{Client: fake.NewClientBuilder().WithScheme(testScheme()).Build()}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	_, err := v.ValidateCreate(context.Background(), schedule)
	if err != nil {
		t.Errorf("expected no error for valid schedule, got: %v", err)
	}
}

func TestNamespaceScheduleValidator_InvalidCron(t *testing.T) {
	v := &LightsOutNamespaceScheduleValidator{Client: fake.NewClientBuilder().WithScheme(testScheme()).Build()}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "not-a-cron",
				Downscale: "0 18 * * *",
			},
		},
	}
	_, err := v.ValidateCreate(context.Background(), schedule)
	if err == nil {
		t.Error("expected validation error for invalid cron expression")
	}
}

func TestNamespaceScheduleValidator_NoNamespaceSelectorRequired(t *testing.T) {
	// Namespace-scoped schedules don't need namespace selection fields.
	// A valid schedule with only cron fields should pass.
	v := &LightsOutNamespaceScheduleValidator{Client: fake.NewClientBuilder().WithScheme(testScheme()).Build()}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "minimal", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	_, err := v.ValidateCreate(context.Background(), schedule)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNamespaceScheduleValidator_WarnIfGlobalScheduleClaims(t *testing.T) {
	// A LightsOutSchedule that targets the same namespace should produce a warning (not rejection).
	scheme := testScheme()
	existing := &lightsoutv1alpha1.LightsOutSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: lightsoutv1alpha1.LightsOutScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
			Namespaces: []string{"team-a"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	v := &LightsOutNamespaceScheduleValidator{Client: c}

	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	warnings, err := v.ValidateCreate(context.Background(), schedule)
	if err != nil {
		t.Errorf("expected no rejection error, got: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning because a global LightsOutSchedule targets this namespace")
	}
}

func TestNamespaceScheduleValidator_UpdateInvalidCron(t *testing.T) {
	v := &LightsOutNamespaceScheduleValidator{Client: fake.NewClientBuilder().WithScheme(testScheme()).Build()}
	old := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	updated := old.DeepCopy()
	updated.Spec.Upscale = "not-a-cron"

	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected validation error for invalid cron on update")
	}
}

func TestNamespaceScheduleValidator_InvalidArgoCDNamespace(t *testing.T) {
	v := &LightsOutNamespaceScheduleValidator{Client: fake.NewClientBuilder().WithScheme(testScheme()).Build()}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
				ArgoCD: &lightsoutv1alpha1.ArgoCDConfig{
					Namespace: "INVALID_NS!",
				},
			},
		},
	}
	_, err := v.ValidateCreate(context.Background(), schedule)
	if err == nil {
		t.Error("expected validation error for invalid ArgoCD namespace name")
	}
}

func TestNamespaceScheduleDefaulter_SetsTimezone(t *testing.T) {
	d := &LightsOutNamespaceScheduleDefaulter{}
	schedule := &lightsoutv1alpha1.LightsOutNamespaceSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	_ = d.Default(context.Background(), schedule)
	if schedule.Spec.Timezone != "UTC" {
		t.Errorf("expected timezone to be set to UTC, got: %q", schedule.Spec.Timezone)
	}
}
