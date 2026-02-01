package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

// WorkloadType identifies the type of Kubernetes workload
type WorkloadType string

const (
	WorkloadTypeDeployment  WorkloadType = "Deployment"
	WorkloadTypeStatefulSet WorkloadType = "StatefulSet"
	WorkloadTypeCronJob     WorkloadType = "CronJob"
)

// Workload represents a single workload to be scaled
type Workload struct {
	Type        WorkloadType
	Name        string
	Namespace   string
	Deployment  *appsv1.Deployment
	StatefulSet *appsv1.StatefulSet
	CronJob     *batchv1.CronJob
}

// WorkloadFromDeployment creates a Workload from a Deployment
func WorkloadFromDeployment(d *appsv1.Deployment) Workload {
	return Workload{
		Type:       WorkloadTypeDeployment,
		Name:       d.Name,
		Namespace:  d.Namespace,
		Deployment: d,
	}
}

// WorkloadFromStatefulSet creates a Workload from a StatefulSet
func WorkloadFromStatefulSet(s *appsv1.StatefulSet) Workload {
	return Workload{
		Type:        WorkloadTypeStatefulSet,
		Name:        s.Name,
		Namespace:   s.Namespace,
		StatefulSet: s,
	}
}

// WorkloadFromCronJob creates a Workload from a CronJob
func WorkloadFromCronJob(c *batchv1.CronJob) Workload {
	return Workload{
		Type:      WorkloadTypeCronJob,
		Name:      c.Name,
		Namespace: c.Namespace,
		CronJob:   c,
	}
}

// ChunkWorkloads splits workloads into batches based on rate limit config.
// If rateLimit is nil or BatchSize is not set, returns all workloads in a single batch.
func ChunkWorkloads(workloads []Workload, rateLimit *lightsoutv1alpha1.RateLimitConfig) [][]Workload {
	if len(workloads) == 0 {
		return nil
	}

	// No rate limiting - return all in one batch
	if rateLimit == nil || rateLimit.BatchSize == nil {
		return [][]Workload{workloads}
	}

	batchSize := *rateLimit.BatchSize
	if batchSize <= 0 {
		return [][]Workload{workloads}
	}

	var batches [][]Workload
	for i := 0; i < len(workloads); i += batchSize {
		end := i + batchSize
		if end > len(workloads) {
			end = len(workloads)
		}
		batches = append(batches, workloads[i:end])
	}

	return batches
}
