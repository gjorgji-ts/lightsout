package controller

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// PeriodResult contains the calculated period state and transition times
type PeriodResult struct {
	State         string
	NextUpscale   time.Time
	NextDownscale time.Time
	LastUpscale   time.Time
	LastDownscale time.Time
}

// cachedPeriod stores a cached period result with its validity window
type cachedPeriod struct {
	result     *PeriodResult
	validUntil time.Time
}

// periodCache is a global cache for period calculations
var (
	periodCache   = make(map[string]*cachedPeriod)
	periodCacheMu sync.RWMutex
)

// scheduleFrequency represents how often a cron schedule triggers
type scheduleFrequency int

const (
	frequencyMinute  scheduleFrequency = iota // Every minute or more frequent
	frequencyHourly                           // Every hour to every minute
	frequencyDaily                            // Every day to every hour
	frequencyWeekly                           // Every week to every day
	frequencyMonthly                          // Every month to every week
	frequencyRare                             // Less frequent than monthly
)

// generateCacheKey creates a unique key for the cache based on schedule specs
func generateCacheKey(upscaleCron, downscaleCron, timezone string) string {
	h := sha256.New()
	h.Write([]byte(upscaleCron))
	h.Write([]byte(downscaleCron))
	h.Write([]byte(timezone))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// getCachedPeriod retrieves a cached period result if valid
func getCachedPeriod(key string, now time.Time) (*PeriodResult, bool) {
	periodCacheMu.RLock()
	defer periodCacheMu.RUnlock()

	cached, exists := periodCache[key]
	if !exists {
		return nil, false
	}

	// Check if cache is still valid
	if now.After(cached.validUntil) {
		return nil, false
	}

	return cached.result, true
}

// setCachedPeriod stores a period result in the cache
func setCachedPeriod(key string, result *PeriodResult) {
	periodCacheMu.Lock()
	defer periodCacheMu.Unlock()

	// Determine cache validity: until the next transition time
	validUntil := result.NextUpscale
	if result.NextDownscale.Before(result.NextUpscale) {
		validUntil = result.NextDownscale
	}

	periodCache[key] = &cachedPeriod{
		result:     result,
		validUntil: validUntil,
	}
}

// ClearPeriodCache clears the period calculation cache (useful for testing)
func ClearPeriodCache() {
	periodCacheMu.Lock()
	defer periodCacheMu.Unlock()
	periodCache = make(map[string]*cachedPeriod)
}

// analyzeScheduleFrequency estimates how often a cron schedule triggers
func analyzeScheduleFrequency(cronExpr string) scheduleFrequency {
	fields := strings.Fields(cronExpr)
	if len(fields) != 5 {
		return frequencyRare // Safety fallback
	}

	minute := fields[0]
	hour := fields[1]
	dom := fields[2] // day of month
	month := fields[3]
	dow := fields[4] // day of week

	// Minute-level: minute field is * or */N
	if minute == "*" || strings.HasPrefix(minute, "*/") {
		return frequencyMinute
	}

	// Hourly: specific minute(s), but hour is * or */N
	if hour == "*" || strings.HasPrefix(hour, "*/") {
		return frequencyHourly
	}

	// Daily: specific hour and minute, but day/month are flexible
	if (dom == "*" || strings.Contains(dom, ",") || strings.Contains(dom, "-") || strings.HasPrefix(dom, "*/")) &&
		(month == "*") && (dow == "*") {
		return frequencyDaily
	}

	// Weekly: has specific day of week constraint
	if dow != "*" && month == "*" {
		return frequencyWeekly
	}

	// Monthly: specific day of month, or specific month
	if dom != "*" || month != "*" {
		// Check for rare patterns like Feb 29
		if month != "*" && !strings.Contains(month, ",") && !strings.Contains(month, "-") {
			// Single specific month
			if dom == "29" || dom == "30" || dom == "31" {
				return frequencyRare // Edge case: might not occur in all months
			}
		}
		return frequencyMonthly
	}

	// Default to rare for safety
	return frequencyRare
}

// getSearchWindow returns the duration to search backwards based on frequency
func getSearchWindow(freq scheduleFrequency) time.Duration {
	switch freq {
	case frequencyMinute:
		return 48 * time.Hour // 2 days
	case frequencyHourly:
		return 72 * time.Hour // 3 days
	case frequencyDaily:
		return 240 * time.Hour // 10 days
	case frequencyWeekly:
		return 768 * time.Hour // 32 days
	case frequencyMonthly:
		return 2160 * time.Hour // 90 days
	case frequencyRare:
		return 9600 * time.Hour // 400 days
	default:
		return 8760 * time.Hour // 365 days (safety fallback)
	}
}

// CalculatePeriod determines if we're in an up or down period based on cron expressions
func CalculatePeriod(upscaleCron, downscaleCron, timezone string, now time.Time) (*PeriodResult, error) {
	// Check cache first
	cacheKey := generateCacheKey(upscaleCron, downscaleCron, timezone)
	if cached, ok := getCachedPeriod(cacheKey, now); ok {
		return cached, nil
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	upscaleSched, err := parser.Parse(upscaleCron)
	if err != nil {
		return nil, fmt.Errorf("invalid upscale cron %q: %w", upscaleCron, err)
	}

	downscaleSched, err := parser.Parse(downscaleCron)
	if err != nil {
		return nil, fmt.Errorf("invalid downscale cron %q: %w", downscaleCron, err)
	}

	// Convert now to the specified timezone
	nowInTz := now.In(loc)

	// Find the last upscale and downscale times by searching backwards
	lastUpscale := findLastOccurrence(upscaleSched, upscaleCron, nowInTz)
	lastDownscale := findLastOccurrence(downscaleSched, downscaleCron, nowInTz)

	// Find next occurrences
	nextUpscale := upscaleSched.Next(nowInTz)
	nextDownscale := downscaleSched.Next(nowInTz)

	// Determine current state:
	// If lastUpscale > lastDownscale, we're in "Up" period
	// If lastDownscale > lastUpscale, we're in "Down" period
	// If equal (shouldn't happen normally), default to "Down"
	state := "Down"
	if lastUpscale.After(lastDownscale) {
		state = "Up"
	}

	result := &PeriodResult{
		State:         state,
		NextUpscale:   nextUpscale,
		NextDownscale: nextDownscale,
		LastUpscale:   lastUpscale,
		LastDownscale: lastDownscale,
	}

	// Cache the result
	setCachedPeriod(cacheKey, result)

	return result, nil
}

// findLastOccurrence finds the most recent time the schedule would have triggered
// using an adaptive search window based on the cron expression frequency
func findLastOccurrence(sched cron.Schedule, cronExpr string, from time.Time) time.Time {
	// Analyze the schedule frequency to determine optimal search window
	freq := analyzeScheduleFrequency(cronExpr)
	searchWindow := getSearchWindow(freq)
	searchStart := from.Add(-searchWindow)

	// First check if there's any occurrence in the search window
	firstNext := sched.Next(searchStart)
	if firstNext.After(from) {
		// No occurrences in the search window - schedule hasn't triggered recently
		return from.AddDate(-10, 0, 0) // Sentinel: "never triggered"
	}

	// Linear search forward from searchStart within the adaptive window
	// Calculate max iterations based on window size
	// For minute-level schedules, this could still be high, so we use a reasonable limit
	maxIterations := int(searchWindow.Hours() * 60) // Maximum minutes in the window
	if maxIterations > 525600 {                     // Cap at 1 year worth of minutes
		maxIterations = 525600
	}
	iterations := 0

	last := firstNext
	next := sched.Next(last)

	// Continue until next would be after 'from'
	// This means last is the most recent occurrence at or before 'from'
	for !next.After(from) && iterations < maxIterations {
		last = next
		next = sched.Next(next)
		iterations++
	}

	return last
}
