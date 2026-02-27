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
)
