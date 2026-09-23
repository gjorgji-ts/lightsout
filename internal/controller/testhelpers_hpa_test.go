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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// testScheduleName is a reusable schedule name for HPA unit tests.
const testScheduleName = "my-schedule"

// makeHPA creates an unstructured HPA for testing.
// minReplicas=nil means the field is absent (Kubernetes default=1).
func makeHPA(targetKind, targetName string, minReplicas *int64, annotations map[string]string) *unstructured.Unstructured {
	hpa := &unstructured.Unstructured{}
	hpa.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "autoscaling",
		Version: "v2",
		Kind:    "HorizontalPodAutoscaler",
	})
	hpa.SetNamespace("ns")
	hpa.SetName("my-hpa")
	if annotations != nil {
		hpa.SetAnnotations(annotations)
	}
	_ = unstructured.SetNestedField(hpa.Object, targetKind, "spec", "scaleTargetRef", "kind")
	_ = unstructured.SetNestedField(hpa.Object, targetName, "spec", "scaleTargetRef", "name")
	if minReplicas != nil {
		_ = unstructured.SetNestedField(hpa.Object, *minReplicas, "spec", "minReplicas")
	}
	return hpa
}

func minReplicasPtr(v int64) *int64 { return &v }

// addHPATypes registers autoscaling/v2 HPA types into an existing scheme.
func addHPATypes(s *runtime.Scheme) {
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler",
	}, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscalerList",
	}, &unstructured.UnstructuredList{})
}

// hpaScheme returns a scheme with only HPA types registered.
func hpaScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	addHPATypes(s)
	return s
}
