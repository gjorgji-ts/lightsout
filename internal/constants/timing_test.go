package constants

import (
	"testing"
	"time"
)

func TestTimingConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      time.Duration
		expected time.Duration
	}{
		{"WarmupCheckInterval", WarmupCheckInterval, 30 * time.Second},
		{"DefaultWarmupTimeout", DefaultWarmupTimeout, 10 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}
