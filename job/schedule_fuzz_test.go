//go:build go1.18
// +build go1.18

package job

// Fuzzing needs Go 1.18 or later (testing.F). Kala's CI still builds with Go
// 1.17, so this file is only compiled on newer Go.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// For any schedule string, Init must not panic or leave the lock held,
// and must either return an error (cache unchanged) or accept the job (cached
// with a timer).
//
// The two v1 flaws this fuzzer originally found are fixed and no longer
// skipped: a start after the year 2262 (which overflowed UnixNano in
// GetWaitDuration) is rejected by InitDelayDuration, and a repeating job
// with a zero-length period (which spun in ResumeAtNextScheduledTime) is
// rejected as well.
func FuzzInitSchedule(f *testing.F) {
	for _, seed := range []string{
		"X/2030-01-01T00:00:00Z/PT1H",
		"R/2030-01-01T00:00:00Z/PT1H",
		"R0/2030-01-01T00:00:00Z/",
		"R-5/2030-01-01T00:00:00Z/PT1H",
		"R2/2030-01-01T00:00:00/P1DT10M10S",
		"R/2300-01-01T00:00:00Z/PT1H",
		"R/2030-01-01T00:00:00Z/PT0S",
		"R//", "///", "R/", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, schedule string) {
		if schedule == "" {
			t.Skip("an empty schedule means run now; not a schedule string")
		}
		cache := NewMockCache()
		j := GetMockJob()
		j.Schedule = schedule

		err := j.Init(cache)
		if err != nil {
			assert.Empty(t, cache.GetAll().Jobs, "rejected schedule %q left a job in the cache", schedule)
		} else {
			assert.Len(t, cache.GetAll().Jobs, 1)
			assert.NotNil(t, j.jobTimer, "accepted schedule %q has no timer", schedule)
		}
		assertLockUsable(t, j)
	})
}
