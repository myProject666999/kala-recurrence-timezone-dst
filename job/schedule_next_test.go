package job

// Tests for the pure recurrence-time functions (schedule.go) and their
// wiring into InitDelayDuration / GetWaitDuration / StartWaiting.
//
// All tests use fixed locations (time.LoadLocation) and the mixer/clock mock
// clock; none of them depend on the system default timezone. If the IANA tz
// data is not installed in the test environment, the timezone tests skip
// with an explicit message.

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ajvb/kala/utils/iso8601"
	"github.com/mixer/clock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("IANA timezone data for %q is not installed in this environment: %v", name, err)
	}
	return loc
}

// assertWallClock asserts that got shows exactly the given local wall clock
// and offset (e.g. "2024-03-31T09:00:00+02:00"). Comparing the formatted
// string avoids time.Time's location-pointer identity while still pinning
// the offset, which is what the DST rules are about.
func assertWallClock(t *testing.T, wantRFC3339 string, got time.Time) {
	t.Helper()
	assert.Equal(t, wantRFC3339, got.Format(time.RFC3339))
}

func mustDuration(t *testing.T, s string) *iso8601.Duration {
	t.Helper()
	d, err := iso8601.FromString(s)
	require.NoError(t, err, s)
	return d
}

func mustRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err, s)
	return parsed
}

// Month-end clamping: the anchor day-of-month is preserved and clamped to
// the target month's last day, so a 31st anchor visits Feb 28 and returns to
// Mar 31. Twelve consecutive occurrences from a Jan 31 anchor, plus the
// leap-day February of 2024.
func TestOccurrenceMonthlyClamping(t *testing.T) {
	anchor := mustRFC3339(t, "2023-01-31T09:00:00Z")
	p1m := mustDuration(t, "P1M")
	expected := []string{
		"2023-01-31T09:00:00Z", // n=0, the anchor itself
		"2023-02-28T09:00:00Z",
		"2023-03-31T09:00:00Z",
		"2023-04-30T09:00:00Z",
		"2023-05-31T09:00:00Z",
		"2023-06-30T09:00:00Z",
		"2023-07-31T09:00:00Z",
		"2023-08-31T09:00:00Z",
		"2023-09-30T09:00:00Z",
		"2023-10-31T09:00:00Z",
		"2023-11-30T09:00:00Z",
		"2023-12-31T09:00:00Z",
		"2024-01-31T09:00:00Z", // n=12
		"2024-02-29T09:00:00Z", // leap year
		"2024-03-31T09:00:00Z",
	}
	for n, want := range expected {
		got := Occurrence(anchor, p1m, nil, int64(n))
		assertWallClock(t, want, got)
	}
}

// P1Y from a leap day clamps to Feb 28 in common years and returns to
// Feb 29 in the next leap year.
func TestOccurrenceYearlyFromLeapDay(t *testing.T) {
	anchor := mustRFC3339(t, "2020-02-29T09:00:00Z")
	p1y := mustDuration(t, "P1Y")
	expected := []string{
		"2021-02-28T09:00:00Z",
		"2022-02-28T09:00:00Z",
		"2023-02-28T09:00:00Z",
		"2024-02-29T09:00:00Z",
	}
	for n, want := range expected {
		got := Occurrence(anchor, p1y, nil, int64(n+1))
		assertWallClock(t, want, got)
	}
}

// P1D keeps the wall clock across DST switches; PT24H is elapsed time and
// lands an hour off on both switch days. Berlin springs forward on
// 2024-03-31 (02:00 -> 03:00) and falls back on 2024-10-27 (03:00 -> 02:00).
func TestP1DVersusPT24HAroundDST(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	p1d := mustDuration(t, "P1D")
	pt24h := mustDuration(t, "PT24H")

	t.Run("spring forward: P1D keeps 09:00 local, PT24H lands at 10:00 local", func(t *testing.T) {
		anchor := mustRFC3339(t, "2024-03-30T09:00:00+01:00")
		gotDay := Occurrence(anchor, p1d, berlin, 1)
		assertWallClock(t, "2024-03-31T09:00:00+02:00", gotDay)
		assertWallClock(t, "2024-03-31T07:00:00Z", gotDay.UTC())

		gotHours := Occurrence(anchor, pt24h, berlin, 1)
		assertWallClock(t, "2024-03-31T10:00:00+02:00", gotHours)
		assertWallClock(t, "2024-03-31T08:00:00Z", gotHours.UTC())
	})

	t.Run("fall back: P1D keeps 09:00 local, PT24H lands at 08:00 local", func(t *testing.T) {
		anchor := mustRFC3339(t, "2024-10-26T09:00:00+02:00")
		gotDay := Occurrence(anchor, p1d, berlin, 1)
		assertWallClock(t, "2024-10-27T09:00:00+01:00", gotDay)
		assertWallClock(t, "2024-10-27T08:00:00Z", gotDay.UTC())

		gotHours := Occurrence(anchor, pt24h, berlin, 1)
		assertWallClock(t, "2024-10-27T08:00:00+01:00", gotHours)
		assertWallClock(t, "2024-10-27T07:00:00Z", gotHours.UTC())
	})
}

// Without a timezone the legacy fixed-offset behavior is unchanged: P1D from
// 2024-03-30T09:00:00+01:00 lands on 2024-03-31T09:00:00+01:00 (UTC 08:00),
// which is 10:00 Berlin summer time.
func TestOccurrenceWithoutTimezoneKeepsFixedOffset(t *testing.T) {
	anchor := mustRFC3339(t, "2024-03-30T09:00:00+01:00")
	got := Occurrence(anchor, mustDuration(t, "P1D"), nil, 1)
	assertWallClock(t, "2024-03-31T09:00:00+01:00", got)
	assertWallClock(t, "2024-03-31T08:00:00Z", got.UTC())
	_, offset := got.Zone()
	assert.Equal(t, 3600, offset, "fixed +01:00 offset is kept")
}

// A local time inside the spring-forward gap does not exist; it shifts
// forward to the same offset position after the gap (02:30 -> 03:30).
func TestOccurrenceGapShiftsForward(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	anchor := mustRFC3339(t, "2024-03-30T02:30:00+01:00")
	got := Occurrence(anchor, mustDuration(t, "P1D"), berlin, 1)
	assertWallClock(t, "2024-03-31T03:30:00+02:00", got)
}

// A local time inside the fall-back overlap occurs twice; the first
// occurrence (still on the pre-transition +02:00 offset) wins.
func TestOccurrenceOverlapTakesFirst(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	anchor := mustRFC3339(t, "2024-10-26T02:30:00+02:00")
	got := Occurrence(anchor, mustDuration(t, "P1D"), berlin, 1)
	assertWallClock(t, "2024-10-27T02:30:00+02:00", got)
	_, offset := got.Zone()
	assert.Equal(t, 7200, offset, "first of the two 02:30 occurrences (still DST)")
	assertWallClock(t, "2024-10-27T00:30:00Z", got.UTC())
}

// NextRunTime must return exactly the sequence that enumerating
// "anchor's nth occurrence" produces — including for anchors far in the
// past, where the old step-from-previous accumulation drifted. The
// enumeration is the baseline; the direct binary search must agree with it.
func TestNextRunTimeMatchesOccurrenceEnumeration(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	cases := []struct {
		name   string
		anchor string
		period string
		loc    *time.Location
	}{
		{"monthly from Jan 31 2015 (clamping)", "2015-01-31T09:00:00Z", "P1M", nil},
		{"daily across many DST switches", "2010-03-15T09:00:00+01:00", "P1D", berlin},
		{"hourly", "2010-06-01T00:00:00Z", "PT1H", nil},
		{"weekly", "2012-02-29T12:00:00Z", "P1W", nil},
		{"yearly from leap day", "2016-02-29T08:30:00Z", "P1Y", nil},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			anchor := mustRFC3339(t, c.anchor)
			period := mustDuration(t, c.period)

			// Baseline: enumerate the occurrences directly from the anchor.
			const count = 140
			occurrences := make([]time.Time, count+1)
			for n := int64(0); n <= count; n++ {
				occurrences[n] = Occurrence(anchor, period, c.loc, n)
				if n > 0 {
					assert.True(t, occurrences[n].After(occurrences[n-1]),
						"occurrences must strictly increase (n=%d)", n)
				}
			}

			for k := 0; k < count; k++ {
				// A reference strictly between two grid points lands on the
				// next grid point.
				ref := occurrences[k].Add(occurrences[k+1].Sub(occurrences[k]) / 2)
				got := NextRunTime(anchor, period, c.loc, ref, false)
				assert.True(t, occurrences[k+1].Equal(got),
					"ref between occurrence %d and %d: want %s, got %s", k, k+1, occurrences[k+1], got)

				// A reference exactly on a grid point: strict mode advances,
				// inclusive mode stays.
				gotStrict := NextRunTime(anchor, period, c.loc, occurrences[k], false)
				assert.True(t, occurrences[k+1].Equal(gotStrict),
					"strict mode on occurrence %d", k)
				gotInclusive := NextRunTime(anchor, period, c.loc, occurrences[k], true)
				assert.True(t, occurrences[k].Equal(gotInclusive),
					"inclusive mode on occurrence %d", k)
			}
		})
	}
}

// A repeating schedule with a zero-length period can never advance and used
// to spin in ResumeAtNextScheduledTime; InitDelayDuration now rejects it.
// The one-off R0 form stays accepted (its period is never parsed).
func TestInitDelayDurationRejectsZeroPeriod(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, period := range []string{"PT0S", "P0D", "P0Y0M0DT0H0M0S"} {
		j := GetMockJob()
		j.Schedule = "R/" + future + "/" + period
		err := j.InitDelayDuration(false)
		require.Error(t, err, period)
		assert.Contains(t, err.Error(), "zero-length repeat period")
	}

	j := GetMockJob()
	j.Schedule = "R0/" + future + "/"
	assert.NoError(t, j.InitDelayDuration(false), "one-off R0 keeps no period")
}

// A start outside the range UnixNano can represent used to wrap the wait
// arithmetic; it is now rejected with a clear error.
func TestInitDelayDurationRejectsOutOfRangeStart(t *testing.T) {
	for _, start := range []string{
		"2300-01-01T00:00:00Z",
		"2262-04-12T00:00:00Z", // just past the UnixNano maximum
		"1600-01-01T00:00:00Z",
	} {
		j := GetMockJob()
		j.Schedule = "R/" + start + "/PT1H"
		err := j.InitDelayDuration(false)
		require.Error(t, err, start)
		assert.Contains(t, err.Error(), "outside the representable range")
	}
}

func TestInitDelayDurationTimezone(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")

	t.Run("unknown timezone is rejected at creation", func(t *testing.T) {
		j := GetMockJob()
		j.Schedule = "R/2027-01-15T09:00:00Z/PT1H"
		j.Timezone = "Not/AZone"
		err := j.InitDelayDuration(false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `cannot load timezone "Not/AZone"`)
	})

	t.Run("offset inconsistent with the location is rejected", func(t *testing.T) {
		j := GetMockJob()
		// Berlin is at +01:00 in January; +02:00 does not match.
		j.Schedule = "R/2027-01-15T09:00:00+02:00/P1D"
		j.Timezone = "Europe/Berlin"
		err := j.InitDelayDuration(false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "offset")
	})

	t.Run("offset consistent with the location is accepted and converted", func(t *testing.T) {
		j := GetMockJob()
		j.Schedule = "R/2027-01-15T09:00:00+01:00/P1D"
		j.Timezone = "Europe/Berlin"
		require.NoError(t, j.InitDelayDuration(false))
		assert.Equal(t, berlin, j.scheduleLocation)
		assert.Equal(t, berlin, j.scheduleTime.Location())
		assert.Equal(t, 9, j.scheduleTime.Hour())
	})

	t.Run("start without offset is interpreted in the timezone", func(t *testing.T) {
		j := GetMockJob()
		j.Schedule = "R/2027-01-15T09:00:00/P1D"
		j.Timezone = "Europe/Berlin"
		require.NoError(t, j.InitDelayDuration(false))
		assertWallClock(t, "2027-01-15T09:00:00+01:00", j.scheduleTime)
	})

	t.Run("empty timezone keeps the fixed offset", func(t *testing.T) {
		j := GetMockJob()
		j.Schedule = "R/2027-01-15T09:00:00+05:00/P1D"
		require.NoError(t, j.InitDelayDuration(false))
		assert.Nil(t, j.scheduleLocation)
		_, offset := j.scheduleTime.Zone()
		assert.Equal(t, 5*3600, offset)
	})
}

// GetWaitDuration computes the run after the last one from the anchor grid:
// a Jan 31 anchor with P1M goes Jan 31 -> Feb 28 -> Mar 31, it does not
// drift to Mar 28/31-by-accumulation.
func TestGetWaitDurationUsesAnchorGrid(t *testing.T) {
	j := GetMockJob()
	j.Schedule = "R/2023-01-31T09:00:00Z/P1M"
	require.NoError(t, j.InitDelayDuration(false))

	now := mustRFC3339(t, "2023-02-28T09:00:00Z")
	j.SetClock(clock.NewMockClock(now))
	j.Metadata.LastAttemptedRun = mustRFC3339(t, "2023-02-28T09:00:00Z") // occurrence 1

	wait := j.GetWaitDuration()
	assert.Equal(t, 31*24*time.Hour, wait, "next run is Mar 31, computed from the anchor")
}

// ResumeAtNextScheduledTime lands on the next grid point at or after now.
func TestGetWaitDurationResumeAtNextScheduledTime(t *testing.T) {
	j := GetMockJob()
	j.Schedule = "R/2023-01-31T09:00:00Z/P1M"
	j.ResumeAtNextScheduledTime = true
	require.NoError(t, j.InitDelayDuration(false))

	now := mustRFC3339(t, "2023-03-01T00:00:00Z")
	j.SetClock(clock.NewMockClock(now))

	wait := j.GetWaitDuration()
	expected := mustRFC3339(t, "2023-03-31T09:00:00Z").Sub(now)
	assert.Equal(t, expected, wait)
}

// StartWaiting's NextRunAt is exactly the pure function's result (same
// instant), not a rounded or re-accumulated value.
func TestStartWaitingNextRunAtEqualsPureFunction(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	j := GetMockJob()
	j.Schedule = "R/2024-03-30T09:00:00+01:00/P1D"
	j.Timezone = "Europe/Berlin"
	require.NoError(t, j.InitDelayDuration(false))

	// The schedule start has passed and the job ran once (at the anchor).
	now := mustRFC3339(t, "2024-03-30T10:00:00+01:00")
	j.SetClock(clock.NewMockClock(now))
	j.Metadata.LastAttemptedRun = mustRFC3339(t, "2024-03-30T09:00:00+01:00")

	cache := NewMockCache()
	j.StartWaiting(cache, false)

	expected := NextRunTime(j.scheduleTime, j.delayDuration, berlin, j.Metadata.LastAttemptedRun, false)
	assertWallClock(t, "2024-03-31T09:00:00+02:00", expected)
	assert.True(t, expected.Equal(j.NextRunAt),
		"NextRunAt %s must be the same instant as the pure function result %s", j.NextRunAt, expected)
}

// Old records have no timezone field: JSON and gob both decode it to the
// empty default, and such jobs load with the legacy fixed-offset behavior.
func TestMissingTimezoneFieldDecodesToDefault(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var j Job
		require.NoError(t, json.Unmarshal([]byte(
			`{"name":"old","command":"echo hi","schedule":"R/2027-01-15T09:00:00+01:00/P1D"}`), &j))
		assert.Equal(t, "", j.Timezone)
		require.NoError(t, j.InitDelayDuration(false))
		assert.Nil(t, j.scheduleLocation)
	})

	t.Run("gob", func(t *testing.T) {
		j := GetMockJob()
		j.Schedule = "R/2027-01-15T09:00:00+01:00/P1D"
		require.NoError(t, j.InitDelayDuration(false))

		var buf bytes.Buffer
		require.NoError(t, gob.NewEncoder(&buf).Encode(j))
		decoded, err := NewFromBytes(buf.Bytes())
		require.NoError(t, err)
		assert.Equal(t, "", decoded.Timezone)
		require.NoError(t, decoded.InitDelayDuration(false))
		assert.Nil(t, decoded.scheduleLocation)
	})
}

// The schedule string form with a timezone travels through the public JSON
// representation without changing next_run_at's offset display for jobs
// that do not set a timezone.
func TestNextRunAtDisplayUnchangedWithoutTimezone(t *testing.T) {
	j := GetMockJob()
	j.Schedule = "R/2027-01-15T09:00:00+05:00/PT1H"
	require.NoError(t, j.InitDelayDuration(false))

	now := mustRFC3339(t, "2027-01-15T08:30:00+05:00")
	j.SetClock(clock.NewMockClock(now))
	j.StartWaiting(NewMockCache(), false)

	// The wait is 30 minutes; NextRunAt is shown in the clock's location
	// (here the mock clock's fixed zone), exactly as before this change.
	assert.Equal(t, 30*time.Minute, j.GetWaitDuration())
	assert.True(t, now.Add(30*time.Minute).Equal(j.NextRunAt))
}

// Sanity check on the pure function inputs: a nil loc uses the anchor's own
// location, and the anchor's wall clock is read in that location.
func TestOccurrenceNilLocationUsesAnchorLocation(t *testing.T) {
	anchor := mustRFC3339(t, "2024-03-30T09:00:00+01:00")
	got := Occurrence(anchor, mustDuration(t, "P1D"), nil, 1)
	_, offset := got.Zone()
	assert.Equal(t, 3600, offset)
	assert.True(t, strings.HasSuffix(got.Format(time.RFC3339), "+01:00"))
}
