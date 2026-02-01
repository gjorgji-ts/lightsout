package controller

import (
	"testing"
	"time"
)

const testTimezoneUTC = "UTC"

func TestCalculatePeriod(t *testing.T) {
	ClearPeriodCache()

	nyLoc, _ := time.LoadLocation("America/New_York")

	tests := []struct {
		name      string
		upscale   string
		downscale string
		timezone  string
		now       time.Time
		wantState string
		wantErr   bool
	}{
		{
			name:      "weekday morning in up period",
			upscale:   "0 6 * * 1-5",
			downscale: "0 18 * * 1-5",
			timezone:  "America/New_York",
			now:       time.Date(2026, 1, 13, 10, 0, 0, 0, nyLoc), // Tuesday 10am
			wantState: "Up",
		},
		{
			name:      "weekday evening in down period",
			upscale:   "0 6 * * 1-5",
			downscale: "0 18 * * 1-5",
			timezone:  "America/New_York",
			now:       time.Date(2026, 1, 13, 20, 0, 0, 0, nyLoc), // Tuesday 8pm
			wantState: "Down",
		},
		{
			name:      "weekend in down period",
			upscale:   "0 6 * * 1-5",
			downscale: "0 18 * * 1-5",
			timezone:  "America/New_York",
			now:       time.Date(2026, 1, 17, 12, 0, 0, 0, nyLoc), // Saturday noon
			wantState: "Down",
		},
		{
			name:      "exactly at upscale time",
			upscale:   "0 6 * * 1-5",
			downscale: "0 18 * * 1-5",
			timezone:  "America/New_York",
			now:       time.Date(2026, 1, 13, 6, 0, 0, 0, nyLoc), // Tuesday 6am
			wantState: "Up",
		},
		{
			name:      "exactly at downscale time",
			upscale:   "0 6 * * 1-5",
			downscale: "0 18 * * 1-5",
			timezone:  "America/New_York",
			now:       time.Date(2026, 1, 13, 18, 0, 0, 0, nyLoc), // Tuesday 6pm
			wantState: "Down",
		},
		{
			name:      "UTC timezone default",
			upscale:   "0 6 * * *",
			downscale: "0 18 * * *",
			timezone:  testTimezoneUTC,
			now:       time.Date(2026, 1, 13, 12, 0, 0, 0, time.UTC),
			wantState: "Up",
		},
		{
			name:      "invalid upscale cron",
			upscale:   "invalid",
			downscale: "0 18 * * *",
			timezone:  testTimezoneUTC,
			now:       time.Now(),
			wantErr:   true,
		},
		{
			name:      "invalid downscale cron",
			upscale:   "0 6 * * *",
			downscale: "invalid",
			timezone:  testTimezoneUTC,
			now:       time.Now(),
			wantErr:   true,
		},
		{
			name:      "invalid timezone",
			upscale:   "0 6 * * *",
			downscale: "0 18 * * *",
			timezone:  "Invalid/Timezone",
			now:       time.Now(),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ClearPeriodCache()
			result, err := CalculatePeriod(tt.upscale, tt.downscale, tt.timezone, tt.now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.State != tt.wantState {
				t.Errorf("state = %v, want %v (lastUpscale=%v, lastDownscale=%v)",
					result.State, tt.wantState, result.LastUpscale, result.LastDownscale)
			}
		})
	}
}

func TestCalculatePeriod_PerformanceWithFrequentSchedules(t *testing.T) {
	ClearPeriodCache()

	// Test that frequent schedules (every minute) don't cause performance issues
	// with the iteration limit in place
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Every minute - would require many iterations without binary search
	result, err := CalculatePeriod("* * * * *", "30 * * * *", testTimezoneUTC, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still calculate state correctly
	if result.State == "" {
		t.Errorf("state should not be empty")
	}

	// Verify that lastUpscale and lastDownscale are reasonable (not 10 years ago)
	tenYearsAgo := now.AddDate(-10, 0, 0)
	if result.LastUpscale.Before(tenYearsAgo.Add(24 * time.Hour)) {
		t.Errorf("lastUpscale is too far in the past: %v", result.LastUpscale)
	}
}

func TestCalculatePeriod_MinuteLevelSchedules(t *testing.T) {
	// Clear cache before tests
	ClearPeriodCache()

	tests := []struct {
		name      string
		upscale   string
		downscale string
		now       time.Time
		wantState string
	}{
		{
			name:      "every minute upscale vs every 5 minutes downscale",
			upscale:   "* * * * *",                                   // Every minute
			downscale: "*/5 * * * *",                                 // Every 5 minutes
			now:       time.Date(2025, 6, 15, 12, 3, 0, 0, time.UTC), // 12:03
			wantState: "Up",                                          // Last upscale at 12:03, last downscale at 12:00
		},
		{
			name:      "every minute downscale vs hourly upscale",
			upscale:   "0 * * * *",                                    // Every hour
			downscale: "* * * * *",                                    // Every minute
			now:       time.Date(2025, 6, 15, 12, 30, 0, 0, time.UTC), // 12:30
			wantState: "Down",                                         // Last upscale at 12:00, last downscale at 12:30
		},
		{
			name:      "every 2 minutes",
			upscale:   "*/2 * * * *",                                 // Every 2 minutes
			downscale: "*/3 * * * *",                                 // Every 3 minutes
			now:       time.Date(2025, 6, 15, 12, 5, 0, 0, time.UTC), // 12:05
			wantState: "Up",                                          // Last upscale at 12:04, last downscale at 12:03
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := CalculatePeriod(tt.upscale, tt.downscale, testTimezoneUTC, tt.now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.State != tt.wantState {
				t.Errorf("state = %v, want %v (lastUpscale=%v, lastDownscale=%v)",
					result.State, tt.wantState, result.LastUpscale, result.LastDownscale)
			}

			// Verify that we found recent occurrences (within the last hour)
			oneHourAgo := tt.now.Add(-1 * time.Hour)
			if result.LastUpscale.Before(oneHourAgo) {
				t.Errorf("lastUpscale %v is too old (more than 1 hour ago)", result.LastUpscale)
			}
			if result.LastDownscale.Before(oneHourAgo) {
				t.Errorf("lastDownscale %v is too old (more than 1 hour ago)", result.LastDownscale)
			}
		})
	}
}

func TestCalculatePeriod_Caching(t *testing.T) {
	// Clear cache before test
	ClearPeriodCache()

	upscale := "0 6 * * 1-5"
	downscale := "0 18 * * 1-5"
	timezone := testTimezoneUTC
	now := time.Date(2025, 6, 16, 10, 0, 0, 0, time.UTC) // Monday 10am

	// First call - should calculate and cache
	result1, err := CalculatePeriod(upscale, downscale, timezone, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second call with same params - should hit cache
	result2, err := CalculatePeriod(upscale, downscale, timezone, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Results should be identical (same pointer if cached)
	if result1 != result2 {
		t.Errorf("expected cached result to be identical, got different pointers")
	}

	if result1.State != result2.State {
		t.Errorf("state mismatch: %v vs %v", result1.State, result2.State)
	}

	// Call with time beyond cache validity (after next transition)
	futureTime := result1.NextDownscale.Add(1 * time.Minute)
	result3, err := CalculatePeriod(upscale, downscale, timezone, futureTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should recalculate and have different state
	if result3.State == result1.State {
		t.Logf("Note: States happen to be the same, but cache should have been invalidated")
	}

	// Verify next transitions are different
	if result3.NextUpscale.Equal(result1.NextUpscale) && result3.NextDownscale.Equal(result1.NextDownscale) {
		t.Errorf("expected different next transitions after cache invalidation")
	}
}

func TestCalculatePeriod_CacheInvalidation(t *testing.T) {
	// Clear cache before test
	ClearPeriodCache()

	upscale := "0 6 * * *"    // 6am daily
	downscale := "0 18 * * *" // 6pm daily
	timezone := testTimezoneUTC

	// Time: 10am (in Up period)
	now1 := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	result1, err := CalculatePeriod(upscale, downscale, timezone, now1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result1.State != "Up" {
		t.Errorf("expected Up state at 10am, got %v", result1.State)
	}

	// Time: 8pm (in Down period) - should invalidate cache
	now2 := time.Date(2025, 6, 15, 20, 0, 0, 0, time.UTC)
	result2, err := CalculatePeriod(upscale, downscale, timezone, now2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result2.State != "Down" {
		t.Errorf("expected Down state at 8pm, got %v", result2.State)
	}

	// Verify cache was invalidated (different results)
	if result1 == result2 {
		t.Errorf("cache should have been invalidated, got same pointer")
	}
}

func TestClearPeriodCache(t *testing.T) {
	// Clear cache at start
	ClearPeriodCache()

	// Add something to cache
	upscale := "0 6 * * *"
	downscale := "0 18 * * *"
	timezone := testTimezoneUTC
	now := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)

	result1, err := CalculatePeriod(upscale, downscale, timezone, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clear cache
	ClearPeriodCache()

	// Call again - should recalculate (different pointer)
	result2, err := CalculatePeriod(upscale, downscale, timezone, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be different pointers (recalculated)
	if result1 == result2 {
		t.Errorf("expected different pointers after cache clear, got same")
	}

	// But same values
	if result1.State != result2.State {
		t.Errorf("state mismatch after cache clear: %v vs %v", result1.State, result2.State)
	}
}
