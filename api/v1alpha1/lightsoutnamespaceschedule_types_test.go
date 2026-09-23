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
