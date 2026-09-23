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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

// ArgoCDConfig configures optional ArgoCD Application CRD labeling.
// When present, lightsout labels ArgoCD Application CRDs to signal
// downscale state, preventing false alerts in ArgoCD UIs.
type ArgoCDConfig struct {
	// Namespace where ArgoCD Application CRDs live.
	// +kubebuilder:default=argocd
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// WarmupTimeout is how long to keep the warming-up label on ArgoCD Application
	// CRDs after upscale before removing it regardless of pod readiness.
	// This prevents ArgoCD alerts from firing while pods are starting up.
	// Defaults to 10 minutes.
	// +kubebuilder:default="10m"
	// +optional
	WarmupTimeout *metav1.Duration `json:"warmupTimeout,omitempty"`
}

// FluxCDConfig configures optional FluxCD Kustomization and HelmRelease suspension.
// When present, lightsout suspends matching Flux resources during downscale to prevent
// reconciliation from restoring scaled-down workloads.
type FluxCDConfig struct {
	// Namespace excluded from the co-located resource search. Resources in this
	// namespace without spec.targetNamespace are skipped because they are system
	// resources, not co-located workload deployments. Defaults to flux-system.
	// +kubebuilder:default=flux-system
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// WarmupTimeout is how long to keep Flux resources suspended after upscale
	// before resuming them regardless of pod readiness.
	// Defaults to 10 minutes.
	// +kubebuilder:default="10m"
	// +optional
	WarmupTimeout *metav1.Duration `json:"warmupTimeout,omitempty"`
}

// FieldPatch sets a single field on a custom resource during downscale.
// The field's previous value is captured so upscale can restore it exactly,
// which matters for replica counts whose desired value lives in Git rather
// than in the schedule.
type FieldPatch struct {
	// Path is an RFC 6901 JSON Pointer to the field, for example
	// "/spec/replicas" or "/metadata/annotations/cnpg.io~1hibernation"
	// ("~1" escapes a literal "/" inside a segment, "~0" a literal "~").
	//
	// A "*" segment matches every element of an array or every key of an
	// object, so "/spec/nodeSets/*/count" targets every node set without
	// depending on their order.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:Pattern=`^/.*$`
	Path string `json:"path"`

	// Value is the value written to Path during downscale.
	// +kubebuilder:validation:Required
	Value apiextensionsv1.JSON `json:"value"`
}

// CustomResourceConfig declares how to turn one kind of operator-managed custom
// resource off during the downscale window.
//
// Workloads created by an operator carry a controller owner reference and are
// skipped by the scaler (see IncludeOwnedWorkloads), because the operator would
// simply reconcile their replica count back. Turning the operator's own custom
// resource off is the supported way to stop them.
type CustomResourceConfig struct {
	// Group is the API group of the custom resource, for example
	// "postgresql.cnpg.io". Empty means the core group.
	// +optional
	Group string `json:"group,omitempty"`

	// Version is the API version of the custom resource, for example "v1".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Kind is the kind of the custom resource, for example "Cluster".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name targets a single resource by name. When empty, every resource of
	// this kind in the schedule's target namespaces is targeted.
	// +optional
	Name string `json:"name,omitempty"`

	// MatchLabels narrows which resources of this kind are targeted.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// SetFields are applied on downscale and reverted on upscale.
	// +optional
	SetFields []FieldPatch `json:"setFields,omitempty"`

	// Delete removes matching resources on downscale instead of patching them,
	// relying on the owning operator to recreate them on upscale. This exists
	// for operators that offer no off switch, such as Strimzi, where the
	// documented way to stop a Kafka cluster is to pause reconciliation and
	// delete the StrimziPodSet resources.
	//
	// Nothing is restored on upscale: recreating the resource is the operator's
	// job once it resumes reconciling. Entries are processed in declaration
	// order, so the entry that pauses the operator must come before the entry
	// that deletes what it manages.
	//
	// Only use this for resources the owning operator rebuilds from a durable
	// spec. Deleting a resource that holds the only copy of its configuration
	// loses it permanently.
	// +optional
	Delete bool `json:"delete,omitempty"`
}

// LightsOutScheduleCore contains the shared scheduling fields used by both
// LightsOutSchedule and LightsOutNamespaceSchedule.
type LightsOutScheduleCore struct {
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

	// IncludeOwnedWorkloads allows scaling workloads that are controlled by
	// another controller (they carry a controller owner reference, e.g. a
	// StatefulSet created by a database operator from its own CRD).
	//
	// By default these are skipped: the owning controller reconciles the replica
	// count back from its custom resource, so lightsout would scale the workload
	// to zero only to have it restored, while its own annotations make subsequent
	// reconciles treat it as already scaled down.
	//
	// Set this to true only when the owning controller has been paused by other
	// means (for example an operator-specific pause or suspend annotation), so
	// nothing will fight the scaling.
	// +kubebuilder:default=false
	// +optional
	IncludeOwnedWorkloads bool `json:"includeOwnedWorkloads,omitempty"`

	// UpscaleRateLimit configures rate limiting when scaling up.
	// If not set, all workloads are scaled up at once.
	// +optional
	UpscaleRateLimit *RateLimitConfig `json:"upscaleRateLimit,omitempty"`

	// DownscaleRateLimit configures rate limiting when scaling down.
	// If not set, all workloads are scaled down at once.
	// +optional
	DownscaleRateLimit *RateLimitConfig `json:"downscaleRateLimit,omitempty"`

	// ArgoCD enables labeling of ArgoCD Application CRDs during scaling
	// operations to signal downscale state. When nil (omitted), ArgoCD
	// integration is disabled.
	// +optional
	ArgoCD *ArgoCDConfig `json:"argoCD,omitempty"`

	// FluxCD enables suspension of FluxCD Kustomization and HelmRelease resources
	// during scaling operations to prevent reconciliation from fighting scale-down.
	// When nil (omitted), FluxCD integration is disabled.
	// +optional
	FluxCD *FluxCDConfig `json:"fluxCD,omitempty"`

	// CustomResources turns operator-managed custom resources off during the
	// downscale window and restores them on upscale. Each entry names one kind
	// and the fields to set on it.
	//
	// Custom resources are turned off before workloads are scaled down, and
	// restored before workloads are scaled up, so databases and message brokers
	// are on their way back before the applications that depend on them start.
	// +optional
	CustomResources []CustomResourceConfig `json:"customResources,omitempty"`

	// CustomResourceWarmupTimeout bounds how long upscale waits for restored
	// custom resources to report ready before scaling application workloads up
	// anyway. Only applies when CustomResources is set. Defaults to 10 minutes.
	// +kubebuilder:default="10m"
	// +optional
	CustomResourceWarmupTimeout *metav1.Duration `json:"customResourceWarmupTimeout,omitempty"`
}

// LightsOutScheduleSpec defines the desired state of LightsOutSchedule
type LightsOutScheduleSpec struct {
	LightsOutScheduleCore `json:",inline"`

	// NamespaceSelector selects namespaces by label
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// Namespaces is an explicit list of namespace names to manage
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludeNamespaces is a list of namespaces to exclude from management
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`
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

	// StuckTerminatingPods counts pods that are still running well past their
	// termination grace period after a downscale. Scaling a workload to zero only
	// writes the spec, so a pod the kubelet cannot kill keeps its node alive while
	// this schedule reports Down. A non-zero value means the namespace did not
	// release the compute it was scaled down to release.
	// +optional
	StuckTerminatingPods int `json:"stuckTerminatingPods,omitempty"`

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
