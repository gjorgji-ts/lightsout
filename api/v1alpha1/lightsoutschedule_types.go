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

// WorkloadType represents a type of Kubernetes workload
// +kubebuilder:validation:Enum=Deployment;StatefulSet;CronJob
type WorkloadType string

const (
	WorkloadTypeDeployment  WorkloadType = "Deployment"
	WorkloadTypeStatefulSet WorkloadType = "StatefulSet"
	WorkloadTypeCronJob     WorkloadType = "CronJob"
)

// ScheduleState represents the current scaling state
// +kubebuilder:validation:Enum=Up;Down;Unknown
type ScheduleState string

const (
	ScheduleStateUp      ScheduleState = "Up"
	ScheduleStateDown    ScheduleState = "Down"
	ScheduleStateUnknown ScheduleState = "Unknown"
)

// RateLimitConfig defines rate limiting for scaling operations
type RateLimitConfig struct {
	// BatchSize is the number of workloads to process before waiting.
	// If not set, all workloads are processed at once (no rate limiting).
	// +kubebuilder:validation:Minimum=1
	// +optional
	BatchSize *int `json:"batchSize,omitempty"`

	// DelayBetweenBatches is the duration to wait between batches.
	// Only applies when BatchSize is set.
	// +optional
	DelayBetweenBatches *metav1.Duration `json:"delayBetweenBatches,omitempty"`
}

// ScalingProgress tracks progress during batched scaling operations
type ScalingProgress struct {
	// Total number of workloads to scale
	Total int `json:"total"`

	// Completed is the number of workloads successfully scaled
	Completed int `json:"completed"`

	// Failed is the number of workloads that failed to scale
	Failed int `json:"failed"`

	// InProgress indicates whether scaling is currently in progress
	InProgress bool `json:"inProgress"`
}

// WorkloadStats contains statistics about managed workloads
type WorkloadStats struct {
	// DeploymentsManaged is the total number of deployments being managed
	DeploymentsManaged int `json:"deploymentsManaged,omitempty"`

	// DeploymentsScaled is the number of deployments currently scaled to 0
	DeploymentsScaled int `json:"deploymentsScaled,omitempty"`

	// StatefulSetsManaged is the total number of statefulsets being managed
	StatefulSetsManaged int `json:"statefulsetsManaged,omitempty"`

	// StatefulSetsScaled is the number of statefulsets currently scaled to 0
	StatefulSetsScaled int `json:"statefulsetsScaled,omitempty"`

	// CronJobsManaged is the total number of cronjobs being managed
	CronJobsManaged int `json:"cronjobsManaged,omitempty"`

	// CronJobsSuspended is the number of cronjobs currently suspended by us
	CronJobsSuspended int `json:"cronjobsSuspended,omitempty"`
}

// LightsOutScheduleSpec defines the desired state of LightsOutSchedule
type LightsOutScheduleSpec struct {
	// Upscale is the cron expression for when to scale workloads up
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Upscale string `json:"upscale"`

	// Downscale is the cron expression for when to scale workloads down
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Downscale string `json:"downscale"`

	// Timezone is the IANA timezone for interpreting cron expressions
	// +kubebuilder:default="UTC"
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// NamespaceSelector selects namespaces by label
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// Namespaces is an explicit list of namespace names to manage
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludeNamespaces is a list of namespaces to exclude from management
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// Suspend pauses all scaling operations when true
	// +kubebuilder:default=false
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// WorkloadTypes specifies which workload types to manage
	// If empty, all types are managed (Deployment, StatefulSet, CronJob)
	// +optional
	WorkloadTypes []WorkloadType `json:"workloadTypes,omitempty"`

	// ExcludeLabels skips workloads matching these labels
	// +optional
	ExcludeLabels *metav1.LabelSelector `json:"excludeLabels,omitempty"`

	// UpscaleRateLimit configures rate limiting when scaling up.
	// If not set, all workloads are scaled up at once.
	// +optional
	UpscaleRateLimit *RateLimitConfig `json:"upscaleRateLimit,omitempty"`

	// DownscaleRateLimit configures rate limiting when scaling down.
	// If not set, all workloads are scaled down at once.
	// +optional
	DownscaleRateLimit *RateLimitConfig `json:"downscaleRateLimit,omitempty"`
}

// LightsOutScheduleStatus defines the observed state of LightsOutSchedule
type LightsOutScheduleStatus struct {
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

	// Namespaces is the list of namespaces currently being managed
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

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
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Upscale",type="string",JSONPath=".spec.upscale"
// +kubebuilder:printcolumn:name="Downscale",type="string",JSONPath=".spec.downscale"
// +kubebuilder:printcolumn:name="Suspended",type="boolean",JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LightsOutSchedule is the Schema for the lightsoutschedules API
type LightsOutSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LightsOutScheduleSpec   `json:"spec,omitempty"`
	Status LightsOutScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LightsOutScheduleList contains a list of LightsOutSchedule
type LightsOutScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LightsOutSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LightsOutSchedule{}, &LightsOutScheduleList{})
}
