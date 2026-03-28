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

	// StateLabel signals the downscale state on ArgoCD Application CRDs
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
)
