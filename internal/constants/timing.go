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

import "time"

const (
	// WarmupCheckInterval is how often the controller re-checks pod readiness
	// while integration resources (ArgoCD apps, FluxCD Kustomizations/HelmReleases)
	// are in the warming-up state
	WarmupCheckInterval = 30 * time.Second

	// DefaultWarmupTimeout is the fallback duration after which the warming-up
	// label is removed regardless of pod readiness, used when no WarmupTimeout
	// is configured on the schedule
	DefaultWarmupTimeout = 10 * time.Minute

	// TerminationGraceSlack is how long past its own grace period a terminating
	// pod is allowed to run before the controller reports it as stuck. The
	// kubelet sends SIGKILL the moment the grace period ends, so a pod that is
	// still present well after that is not shutting down slowly. It is wedged.
	TerminationGraceSlack = 2 * time.Minute

	// TerminationCheckInterval is how often the controller re-counts terminating
	// pods while any are still present. Downscale sets replicas to zero and
	// returns, so this poll is the only thing that notices pods which never go
	// away and keep their nodes alive.
	TerminationCheckInterval = 5 * time.Minute
)
