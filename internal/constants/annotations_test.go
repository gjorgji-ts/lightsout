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

package constants

import "testing"

func TestConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		// Prefixes
		{"AnnotationPrefix", AnnotationPrefix, "lightsout.techsupport.mk/"},
		{"LabelPrefix", LabelPrefix, "lightsout.techsupport.mk/"},

		// Annotations
		{"OriginalReplicasAnnotation", OriginalReplicasAnnotation, "lightsout.techsupport.mk/original-replicas"},
		{"OriginalSuspendAnnotation", OriginalSuspendAnnotation, "lightsout.techsupport.mk/original-suspend"},
		{"ManagedByAnnotation", ManagedByAnnotation, "lightsout.techsupport.mk/managed-by"},

		// Labels
		{"ManagedByLabel", ManagedByLabel, "lightsout.techsupport.mk/managed-by"},

		// Static values
		{"SuspendedByLightsOut", SuspendedByLightsOut, "lightsout"},
		{"SuspendedByUser", SuspendedByUser, "user"},
		{"OperationDownscale", OperationDownscale, "downscale"},
		{"OperationUpscale", OperationUpscale, "upscale"},
		{"FinalizerName", FinalizerName, "lightsout.techsupport.mk/cleanup"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestAnnotationsUseCorrectPrefix(t *testing.T) {
	annotations := []string{
		OriginalReplicasAnnotation,
		OriginalSuspendAnnotation,
		ManagedByAnnotation,
		FinalizerName,
	}

	for _, ann := range annotations {
		if len(ann) <= len(AnnotationPrefix) {
			t.Errorf("annotation %q is too short", ann)
			continue
		}
		if ann[:len(AnnotationPrefix)] != AnnotationPrefix {
			t.Errorf("annotation %q does not use AnnotationPrefix %q", ann, AnnotationPrefix)
		}
	}
}

func TestLabelsUseCorrectPrefix(t *testing.T) {
	labels := []string{
		ManagedByLabel,
	}

	for _, label := range labels {
		if len(label) <= len(LabelPrefix) {
			t.Errorf("label %q is too short", label)
			continue
		}
		if label[:len(LabelPrefix)] != LabelPrefix {
			t.Errorf("label %q does not use LabelPrefix %q", label, LabelPrefix)
		}
	}
}
