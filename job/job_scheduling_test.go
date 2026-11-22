// Tests for scheduling code.
package job

import (
	"time"

	"github.com/mixer/clock"

	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GetWaitDuration is exercised with a fixed mock clock so the expected
// values can be worked out by hand. The grid is computed from the anchor:
// the run after the last one is the first grid point strictly after it.
func TestGetWaitDuration(t *testing.T) {
	now := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time // zero means the job never ran
		resume   bool
		expected time.Duration
	}{
		{
			name:     "schedule time still in the future",
			schedule: "R/2020-06-15T12:00:10Z/PT10S",
			expected: 10 * time.Second,
		},
		{
			name:     "passed time, last run on the grid: one full interval",
			schedule: "R/2020-06-15T10:00:00Z/PT10S",
			lastRun:  time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC), // a grid point
			expected: 10 * time.Second,
		},
		{
			name:     "passed time, last run off the grid: snaps to the next grid point",
			schedule: "R/2020-06-15T10:00:05Z/PT10S", // grid at :05, :15, ...
			lastRun:  time.Date(2020, 6, 15, 12, 0, 3, 0, time.UTC),
			expected: 5 * time.Second, // next grid point is 12:00:05
		},
		{
			name:     "passed time, 1 minute",
			schedule: "R/2020-06-15T10:00:00Z/PT1M",
			lastRun:  time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC),
			expected: time.Minute,
		},
		{
			name:     "passed time, 1 day",
			schedule: "R/2020-06-14T12:00:00Z/P1D",
			lastRun:  time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC),
			expected: 24 * time.Hour,
		},
		{
			name:     "passed time, never ran: one full interval from now",
			schedule: "R/2020-06-15T10:00:00Z/PT1M",
			expected: time.Minute,
		},
		{
			name:     "one-off R0 with passed time runs immediately",
			schedule: "R0/2020-06-15T10:00:00Z/",
			expected: 0,
		},
		{
			name:     "resume at next scheduled time lands on the grid",
			schedule: "R/2020-06-15T10:30:00Z/PT1H",
			resume:   true,
			expected: 30 * time.Minute, // grid points 10:30, 11:30, 12:30; next at/after 12:00 is 12:30
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			j := GetMockJob()
			j.Schedule = tc.schedule
			j.ResumeAtNextScheduledTime = tc.resume
			require.NoError(t, j.InitDelayDuration(false))
			j.SetClock(clock.NewMockClock(now))
			j.Metadata.LastAttemptedRun = tc.lastRun

			assert.Equal(t, tc.expected, j.GetWaitDuration())
		})
	}
}

// The resume case on an exact grid boundary: the grid point at now counts.
func TestGetWaitDurationResumeOnGridBoundary(t *testing.T) {
	now := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	j := GetMockJob()
	j.Schedule = "R/2020-06-15T12:00:00Z/PT1H"
	j.ResumeAtNextScheduledTime = true
	require.NoError(t, j.InitDelayDuration(false))
	j.SetClock(clock.NewMockClock(now))

	// scheduleTime == now, so the first branch returns a zero wait.
	assert.Equal(t, time.Duration(0), j.GetWaitDuration())
}
