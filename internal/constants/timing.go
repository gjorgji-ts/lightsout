package constants

import "time"

const (
	// WarmupCheckInterval is how often the controller re-checks pod readiness
	// while an ArgoCD app is in the warming-up state
	WarmupCheckInterval = 30 * time.Second

	// DefaultWarmupTimeout is the fallback duration after which the warming-up
	// label is removed regardless of pod readiness, used when no WarmupTimeout
	// is configured on the schedule
	DefaultWarmupTimeout = 10 * time.Minute
)
