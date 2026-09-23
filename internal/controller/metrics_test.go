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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestScalingMetricsRegistered(t *testing.T) {
	// Verify metrics are registered by checking they can be described
	ch := make(chan *prometheus.Desc, 10)

	ScalingBatchesTotal.Describe(ch)
	desc := <-ch
	if desc == nil {
		t.Error("ScalingBatchesTotal not registered")
	}

	ScalingWorkloadsProcessed.Describe(ch)
	desc = <-ch
	if desc == nil {
		t.Error("ScalingWorkloadsProcessed not registered")
	}

	ScalingDurationSeconds.Describe(ch)
	desc = <-ch
	if desc == nil {
		t.Error("ScalingDurationSeconds not registered")
	}
}
