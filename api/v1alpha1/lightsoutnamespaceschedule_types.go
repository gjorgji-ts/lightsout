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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LightsOutNamespaceScheduleSpec defines the desired state of LightsOutNamespaceSchedule
type LightsOutNamespaceScheduleSpec struct {
	LightsOutScheduleCore `json:",inline"`
}

// LightsOutNamespaceScheduleStatus defines the observed state of LightsOutNamespaceSchedule
type LightsOutNamespaceScheduleStatus struct {
	// State is the current scaling state (Up, Down, or Unknown)
	// +optional
	State ScheduleState `json:"state,omitempty"`

	// LastUpscaleTime is the last time workloads were scaled up
	// +optional
	LastUpscaleTime *metav1.Time `json:"lastUpscaleTime,omitempty"`

	// LastDownscaleTime is the last time workloads were scaled down
	// +optional
	LastDownscaleTime *metav1.Time `json:"lastDownscaleTime,omitempty"`

	// NextUpscaleTime is the next scheduled upscale time
	// +optional
	NextUpscaleTime *metav1.Time `json:"nextUpscaleTime,omitempty"`

	// NextDownscaleTime is the next scheduled downscale time
	// +optional
	NextDownscaleTime *metav1.Time `json:"nextDownscaleTime,omitempty"`

	// ObservedGeneration is the generation last processed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// WorkloadStats contains statistics about managed workloads
	// +optional
	WorkloadStats WorkloadStats `json:"workloadStats,omitempty"`

	// ScalingProgress shows progress during batched scaling operations.
	// Only present while scaling is in progress.
	// +optional
	ScalingProgress *ScalingProgress `json:"scalingProgress,omitempty"`

	// Conditions represent the current state of the schedule
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Upscale",type="string",JSONPath=".spec.upscale"
// +kubebuilder:printcolumn:name="Downscale",type="string",JSONPath=".spec.downscale"
// +kubebuilder:printcolumn:name="Suspended",type="boolean",JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LightsOutNamespaceSchedule is the Schema for namespace-scoped scaling schedules.
// It manages workloads only within the namespace it is created in, and takes
// precedence over any LightsOutSchedule (cluster-scoped) that would otherwise
// manage the same namespace.
type LightsOutNamespaceSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LightsOutNamespaceScheduleSpec   `json:"spec,omitempty"`
	Status LightsOutNamespaceScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LightsOutNamespaceScheduleList contains a list of LightsOutNamespaceSchedule
type LightsOutNamespaceScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LightsOutNamespaceSchedule `json:"items"`
}
