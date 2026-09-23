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
		{"TerminationGraceSlack", TerminationGraceSlack, 2 * time.Minute},
		{"TerminationCheckInterval", TerminationCheckInterval, 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}
