package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

func TestChunkWorkloads_NoBatching(t *testing.T) {
	workloads := []Workload{
		{Type: WorkloadTypeDeployment, Name: "deploy1", Namespace: "ns1"},
		{Type: WorkloadTypeDeployment, Name: "deploy2", Namespace: "ns1"},
		{Type: WorkloadTypeStatefulSet, Name: "sts1", Namespace: "ns1"},
	}

	batches := ChunkWorkloads(workloads, nil)
	if len(batches) != 1 {
		t.Errorf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("expected 3 workloads in batch, got %d", len(batches[0]))
	}
}

func TestChunkWorkloads_WithBatching(t *testing.T) {
	workloads := []Workload{
		{Type: WorkloadTypeDeployment, Name: "deploy1", Namespace: "ns1"},
		{Type: WorkloadTypeDeployment, Name: "deploy2", Namespace: "ns1"},
		{Type: WorkloadTypeDeployment, Name: "deploy3", Namespace: "ns1"},
		{Type: WorkloadTypeStatefulSet, Name: "sts1", Namespace: "ns1"},
		{Type: WorkloadTypeStatefulSet, Name: "sts2", Namespace: "ns1"},
	}

	batchSize := 2
	rateLimit := &lightsoutv1alpha1.RateLimitConfig{
		BatchSize: &batchSize,
	}

	batches := ChunkWorkloads(workloads, rateLimit)
	if len(batches) != 3 {
		t.Errorf("expected 3 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 workloads in first batch, got %d", len(batches[0]))
	}
	if len(batches[1]) != 2 {
		t.Errorf("expected 2 workloads in second batch, got %d", len(batches[1]))
	}
	if len(batches[2]) != 1 {
		t.Errorf("expected 1 workload in third batch, got %d", len(batches[2]))
	}
}

func TestChunkWorkloads_EmptyList(t *testing.T) {
	var workloads []Workload
	batchSize := 10
	rateLimit := &lightsoutv1alpha1.RateLimitConfig{
		BatchSize: &batchSize,
	}

	batches := ChunkWorkloads(workloads, rateLimit)
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for empty list, got %d", len(batches))
	}
}

func TestChunkWorkloads_BatchSizeLargerThanList(t *testing.T) {
	workloads := []Workload{
		{Type: WorkloadTypeDeployment, Name: "deploy1", Namespace: "ns1"},
		{Type: WorkloadTypeDeployment, Name: "deploy2", Namespace: "ns1"},
	}

	batchSize := 100
	rateLimit := &lightsoutv1alpha1.RateLimitConfig{
		BatchSize: &batchSize,
	}

	batches := ChunkWorkloads(workloads, rateLimit)
	if len(batches) != 1 {
		t.Errorf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 workloads in batch, got %d", len(batches[0]))
	}
}

func TestWorkloadFromDeployment(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromDeployment(deploy)
	if workload.Type != WorkloadTypeDeployment {
		t.Errorf("expected type Deployment, got %s", workload.Type)
	}
	if workload.Name != "my-deploy" {
		t.Errorf("expected name my-deploy, got %s", workload.Name)
	}
	if workload.Namespace != "my-ns" {
		t.Errorf("expected namespace my-ns, got %s", workload.Namespace)
	}
	if workload.Deployment != deploy {
		t.Error("expected Deployment pointer to be set")
	}
}

func TestWorkloadFromStatefulSet(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-sts",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromStatefulSet(sts)
	if workload.Type != WorkloadTypeStatefulSet {
		t.Errorf("expected type StatefulSet, got %s", workload.Type)
	}
	if workload.StatefulSet != sts {
		t.Error("expected StatefulSet pointer to be set")
	}
}

func TestWorkloadFromCronJob(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cj",
			Namespace: "my-ns",
		},
	}

	workload := WorkloadFromCronJob(cj)
	if workload.Type != WorkloadTypeCronJob {
		t.Errorf("expected type CronJob, got %s", workload.Type)
	}
	if workload.CronJob != cj {
		t.Error("expected CronJob pointer to be set")
	}
}
