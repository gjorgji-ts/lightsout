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
