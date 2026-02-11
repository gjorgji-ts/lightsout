package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
