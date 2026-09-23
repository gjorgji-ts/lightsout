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

package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

// integrationConfig carries the per-reconciler values the integration helpers need,
// so both the cluster-scoped and namespace-scoped reconcilers drive them identically.
type integrationConfig struct {
	// ScheduleObj is the schedule resource events are recorded against.
	ScheduleObj runtime.Object
	// Core holds the integration settings shared by both schedule kinds.
	Core *lightsoutv1alpha1.LightsOutScheduleCore
	// ScheduleName is used for ownership labels and annotations.
	ScheduleName string
	// ScheduleLabel is the Prometheus metric label. Global schedules use the name.
	// Namespace schedules use "namespace/name" to avoid collisions.
	ScheduleLabel string
	// Namespaces is the set of namespaces to process.
	Namespaces []string
}

// integrationsDown turns every enabled integration off ahead of workload scale-down.
//
// Ordering matters: GitOps controllers and operators all reconcile workloads back to
// their declared replica counts, so each must be told to stop before the scaler zeroes
// anything. Failures are logged and surfaced as events but never block scaling.
func integrationsDown(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	cfg integrationConfig,
) {
	if cfg.Core.ArgoCD != nil {
		labelArgoCDAppsDown(ctx, c, recorder, cfg.ScheduleObj, cfg.Core.ArgoCD, cfg.ScheduleName, cfg.Namespaces)
	}
	if cfg.Core.FluxCD != nil {
		labelFluxResourcesDown(ctx, c, recorder, cfg.ScheduleObj, cfg.Core.FluxCD, cfg.ScheduleName, cfg.Namespaces)
	}
	if len(cfg.Core.CustomResources) > 0 {
		customResourcesDown(ctx, c, recorder, cfg.ScheduleObj, cfg.Core, cfg.ScheduleName, cfg.ScheduleLabel, cfg.Namespaces)
	}
}

// integrationsUp restores custom resources ahead of workload scale-up and reports
// whether the scaler must wait another cycle.
//
// Databases and message brokers are brought back first and given time to become
// ready, so the applications that depend on them are not started against a service
// that is still hibernating or still waiting on a node.
func integrationsUp(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	cfg integrationConfig,
	now time.Time,
) bool {
	if len(cfg.Core.CustomResources) == 0 {
		return false
	}

	if customResourcesUp(ctx, c, recorder, cfg.ScheduleObj, cfg.Core, cfg.ScheduleName, cfg.ScheduleLabel, cfg.Namespaces, now) {
		// Something was just restored. Report it as still warming up rather than
		// checking readiness against an object the cache has not caught up with yet.
		return true
	}
	return handleCustomResourceWarmup(ctx, c, recorder, cfg.ScheduleObj, cfg.Core, cfg.ScheduleName, cfg.Namespaces, now)
}

// integrationsWarmup drives the ArgoCD and FluxCD warming-up state machines after
// workloads have been scaled up, and reports whether either is still waiting.
//
// Only call this once workloads are actually up: both integrations gate on workload
// readiness, so running them while workloads are still deliberately scaled down would
// resume them too early.
func integrationsWarmup(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	cfg integrationConfig,
	now time.Time,
) bool {
	stillWarmingUp := false

	if cfg.Core.ArgoCD != nil {
		argoWarmingUp := handleArgoCDWarmup(ctx, c, recorder, cfg.ScheduleObj, cfg.Core.ArgoCD, cfg.ScheduleName, cfg.Namespaces, now)
		stillWarmingUp = stillWarmingUp || argoWarmingUp
	}
	if cfg.Core.FluxCD != nil {
		fluxWarmingUp := handleFluxCDWarmup(ctx, c, recorder, cfg.ScheduleObj, cfg.Core.FluxCD, cfg.ScheduleName, cfg.Namespaces, now)
		stillWarmingUp = stillWarmingUp || fluxWarmingUp
	}

	return stillWarmingUp
}
