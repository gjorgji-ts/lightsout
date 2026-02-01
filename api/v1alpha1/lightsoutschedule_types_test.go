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

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRateLimitConfig_Defaults(t *testing.T) {
	// RateLimitConfig with nil values should be valid (no rate limiting)
	config := &RateLimitConfig{}
	if config.BatchSize != nil {
		t.Error("BatchSize should be nil by default")
	}
	if config.DelayBetweenBatches != nil {
		t.Error("DelayBetweenBatches should be nil by default")
	}
}

func TestRateLimitConfig_WithValues(t *testing.T) {
	batchSize := 10
	delay := metav1.Duration{Duration: 5 * time.Second}
	config := &RateLimitConfig{
		BatchSize:           &batchSize,
		DelayBetweenBatches: &delay,
	}
	if *config.BatchSize != 10 {
		t.Errorf("BatchSize = %d, want 10", *config.BatchSize)
	}
	if config.DelayBetweenBatches.Duration != 5*time.Second {
		t.Errorf("DelayBetweenBatches = %v, want 5s", config.DelayBetweenBatches.Duration)
	}
}

func TestScalingProgress_Fields(t *testing.T) {
	progress := ScalingProgress{
		Total:      100,
		Completed:  50,
		Failed:     2,
		InProgress: true,
	}
	if progress.Total != 100 {
		t.Errorf("Total = %d, want 100", progress.Total)
	}
	if progress.Completed != 50 {
		t.Errorf("Completed = %d, want 50", progress.Completed)
	}
	if progress.Failed != 2 {
		t.Errorf("Failed = %d, want 2", progress.Failed)
	}
	if !progress.InProgress {
		t.Error("InProgress should be true")
	}
}

func TestLightsOutScheduleSpec_RateLimitFields(t *testing.T) {
	batchSize := 10
	delay := metav1.Duration{Duration: 5 * time.Second}

	spec := LightsOutScheduleSpec{
		Upscale:   "0 6 * * 1-5",
		Downscale: "0 18 * * 1-5",
		UpscaleRateLimit: &RateLimitConfig{
			BatchSize:           &batchSize,
			DelayBetweenBatches: &delay,
		},
		DownscaleRateLimit: &RateLimitConfig{
			BatchSize: &batchSize,
		},
	}

	if spec.UpscaleRateLimit == nil {
		t.Error("UpscaleRateLimit should not be nil")
	}
	if spec.DownscaleRateLimit == nil {
		t.Error("DownscaleRateLimit should not be nil")
	}
	if *spec.UpscaleRateLimit.BatchSize != 10 {
		t.Errorf("UpscaleRateLimit.BatchSize = %d, want 10", *spec.UpscaleRateLimit.BatchSize)
	}
}

func TestLightsOutScheduleStatus_ScalingProgress(t *testing.T) {
	status := LightsOutScheduleStatus{
		State: ScheduleStateDown,
		ScalingProgress: &ScalingProgress{
			Total:      100,
			Completed:  75,
			Failed:     3,
			InProgress: true,
		},
	}

	if status.ScalingProgress == nil {
		t.Error("ScalingProgress should not be nil")
	}
	if status.ScalingProgress.Total != 100 {
		t.Errorf("Total = %d, want 100", status.ScalingProgress.Total)
	}
	if status.ScalingProgress.Completed != 75 {
		t.Errorf("Completed = %d, want 75", status.ScalingProgress.Completed)
	}
}
