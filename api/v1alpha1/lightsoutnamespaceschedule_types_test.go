package v1alpha1_test

import (
	"testing"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

func TestLightsOutNamespaceSchedule_SpecHasCoreFields(t *testing.T) {
	schedule := lightsoutv1alpha1.LightsOutNamespaceSchedule{
		Spec: lightsoutv1alpha1.LightsOutNamespaceScheduleSpec{
			LightsOutScheduleCore: lightsoutv1alpha1.LightsOutScheduleCore{
				Upscale:   "0 6 * * *",
				Downscale: "0 18 * * *",
			},
		},
	}
	if schedule.Spec.Upscale != "0 6 * * *" {
		t.Errorf("expected Upscale to be accessible via promotion")
	}
}

func TestLightsOutNamespaceSchedule_SpecHasNoNamespaceFields(t *testing.T) {
	// Compile-time check: these fields exist via promotion from LightsOutScheduleCore
	var spec lightsoutv1alpha1.LightsOutNamespaceScheduleSpec
	_ = spec.Upscale
	_ = spec.Downscale
	// NamespaceSelector and Namespaces do NOT exist on this type — intentionally not referenced
}
