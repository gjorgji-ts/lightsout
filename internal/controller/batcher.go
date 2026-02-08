package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
