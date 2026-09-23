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

// internal/constants/annotations.go
package constants

const (
	// AnnotationPrefix is the prefix for all LightsOut annotations
	AnnotationPrefix = "lightsout.techsupport.mk/"

	// LabelPrefix is the prefix for all LightsOut labels
	LabelPrefix = "lightsout.techsupport.mk/"

	// OriginalReplicasAnnotation stores the original replica count before scaling down
	OriginalReplicasAnnotation = AnnotationPrefix + "original-replicas"

	// OriginalSuspendAnnotation stores who suspended the CronJob ("lightsout" or "user")
	OriginalSuspendAnnotation = AnnotationPrefix + "original-suspend"

	// OriginalHPAScaleUpPolicyAnnotation stores the HPA's original
	// spec.behavior.scaleUp.selectPolicy value before LightsOut sets it to "Disabled"
	// during downscale. Empty string means the field was absent (default behaviour).
	OriginalHPAScaleUpPolicyAnnotation = AnnotationPrefix + "original-hpa-scale-up-policy"

	// OriginalFieldsAnnotation stores the JSON-encoded original values of the fields
	// LightsOut overwrote on a custom resource during downscale, keyed by the concrete
	// JSON Pointer of each field, so upscale can restore them exactly.
	OriginalFieldsAnnotation = AnnotationPrefix + "original-fields"

	// ManagedByAnnotation stores the name of the Schedule managing this workload
	ManagedByAnnotation = AnnotationPrefix + "managed-by"

	// ManagedByLabel enables server-side filtering for managed workloads (indexed by k8s)
	ManagedByLabel = LabelPrefix + "managed-by"

	// SuspendedByLightsOut indicates LightsOut suspended the CronJob
	SuspendedByLightsOut = "lightsout"

	// SuspendedByUser indicates the user suspended the CronJob
	SuspendedByUser = "user"

	// OperationDownscale represents a downscale operation
	OperationDownscale = "downscale"

	// OperationUpscale represents an upscale operation
	OperationUpscale = "upscale"

	// FinalizerName is the finalizer used to ensure cleanup on schedule deletion
	FinalizerName = AnnotationPrefix + "cleanup"

	// StateLabel signals the downscale state on managed integration resources
	// (ArgoCD Application CRDs, FluxCD Kustomization and HelmRelease resources)
	StateLabel = LabelPrefix + "state"

	// StateDown is the value for StateLabel when the app is downscaled
	StateDown = "down"

	// StateWarmingUp is the value for StateLabel while upscaled workloads are becoming ready
	StateWarmingUp = "warming-up"

	// WarmingUpSinceAnnotation stores the RFC3339 timestamp when warming-up began,
	// used to enforce the configurable warmup timeout
	WarmingUpSinceAnnotation = AnnotationPrefix + "warming-up-since"

	// DefaultArgoCDNamespace is the default namespace where ArgoCD Application CRDs live
	DefaultArgoCDNamespace = "argocd"

	// DefaultFluxCDNamespace is the default namespace where FluxCD Kustomization
	// and HelmRelease resources live.
	DefaultFluxCDNamespace = "flux-system"

	// DefaultTimezone is the default IANA timezone used when none is specified on a schedule
	DefaultTimezone = "UTC"

	// EventActionScaleUp is the Kubernetes event action string for a scale-up operation
	EventActionScaleUp = "ScaleUp"

	// EventActionScaleDown is the Kubernetes event action string for a scale-down operation
	EventActionScaleDown = "ScaleDown"

	// EventReasonScaledUp is the Kubernetes event reason string when workloads are scaled up
	EventReasonScaledUp = "ScaledUp"

	// EventReasonScaledDown is the Kubernetes event reason string when workloads are scaled down
	EventReasonScaledDown = "ScaledDown"

	// EventReasonPodsStuckTerminating is the Kubernetes event reason string when
	// pods outlive their termination grace period after a downscale
	EventReasonPodsStuckTerminating = "PodsStuckTerminating"

	// ConditionTypeReady is the status condition type reported on schedules after reconcile
	ConditionTypeReady = "Ready"
)
