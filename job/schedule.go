package job

// Pure recurrence-time computation.
//
// The functions in this file compute run times from explicit inputs only —
// anchor, period, location and a reference time — and never read a clock or
// Job state. Job.GetWaitDuration calls them; tests can call them directly.
//
// Rules (see docs/scheduling.md for the full contract):
//
//   - The nth occurrence is the anchor advanced by n periods, computed from
//     the anchor every time. It never accumulates from the previous
//     (possibly clamped) occurrence, so month-end dates cannot drift.
//   - Years, months, weeks and days are calendar units: they advance the
//     local calendar date and keep the wall clock (P1D keeps 09:00 local
//     across a DST switch). Hours, minutes and seconds are elapsed units
//     (PT24H is always 24 hours and may land an hour off the wall clock).
//   - Month-end clamping: if the anchor's day-of-month does not exist in the
//     target month, the occurrence lands on the last day of that month. The
//     anchor day is remembered, so a 31st anchor returns to the 31st in
//     months that have one (Jan 31 -> Feb 28/29 -> Mar 31).
//   - A local time that does not exist (spring-forward gap) shifts forward
//     to the same offset position after the gap (02:30 -> 03:30 when clocks
//     jump 02:00 -> 03:00); this matches time.Date's documented
//     normalization and is pinned by tests.
//   - A local time that occurs twice (fall-back overlap) resolves to the
//     FIRST occurrence (still on the pre-transition offset). time.Date picks
//     the second, so the correction is applied explicitly.

import (
	"time"

	"github.com/ajvb/kala/utils/iso8601"
)

// maxOccurrenceSearch bounds the binary search in NextRunTime. With the
// smallest representable period (1 second) it covers ~36 million years.
const maxOccurrenceSearch = int64(1) << 50

// Occurrence returns the time of the nth run of a schedule: the anchor
// advanced by n periods. n == 0 is the anchor itself (unless its local time
// falls in a DST gap or overlap, in which case the rules above apply).
//
// If loc is non-nil the anchor is first converted to that location; the
// calendar rules then apply in that location. A nil loc uses the anchor's
// own location (a fixed offset or UTC for schedules without a timezone).
func Occurrence(anchor time.Time, period *iso8601.Duration, loc *time.Location, n int64) time.Time {
	if loc != nil {
		anchor = anchor.In(loc)
	} else {
		loc = anchor.Location()
	}

	// Calendar units: total months from the anchor's year-month, with the
	// anchor's day-of-month clamped to the target month's last day.
	totalMonths := int64(anchor.Year())*12 + int64(anchor.Month()) - 1
	totalMonths += int64(period.Years)*12*n + int64(period.Months)*n
	year := int(totalMonths / 12)
	month := time.Month(totalMonths%12 + 1)

	day := anchor.Day()
	if last := daysIn(year, month); day > last {
		day = last
	}
	// Weeks and days shift the calendar date; time.Date normalizes the
	// overflow (e.g. Jan 31 + 3 days) without touching the wall clock.
	day += (period.Days + period.Weeks*7) * int(n)

	wall := time.Date(year, month, day,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), loc)
	wall = resolveLocalWallClock(wall, loc)

	// Elapsed units: plain elapsed time, insensitive to the calendar. The
	// total shift can exceed the ~292 years a time.Duration holds (e.g. a
	// PT1S period evaluated centuries from the anchor), so it is added in
	// batches.
	seconds := int64(period.Hours)*3600 + int64(period.Minutes)*60 + int64(period.Seconds)
	if seconds != 0 {
		wall = addElapsedSeconds(wall, seconds*n)
	}
	return wall
}

// maxElapsedBatch is the largest shift applied to a time.Time in one Add:
// 100 years, well inside the range of a time.Duration.
const maxElapsedBatch = int64(86400 * 365 * 100)

// addElapsedSeconds adds (possibly very many) elapsed seconds to t.
func addElapsedSeconds(t time.Time, seconds int64) time.Time {
	for seconds != 0 {
		step := seconds
		if step > maxElapsedBatch {
			step = maxElapsedBatch
		}
		if step < -maxElapsedBatch {
			step = -maxElapsedBatch
		}
		t = t.Add(time.Duration(step) * time.Second)
		seconds -= step
	}
	return t
}

// NextRunTime returns the first occurrence strictly after ref — or at ref
// when inclusive is true. Every occurrence is computed from the anchor, so
// the result does not depend on how the previous run was clamped or delayed.
func NextRunTime(anchor time.Time, period *iso8601.Duration, loc *time.Location, ref time.Time, inclusive bool) time.Time {
	atOrAfter := func(t time.Time) bool {
		return t.After(ref) || (inclusive && t.Equal(ref))
	}

	// The anchor itself may already satisfy the condition.
	if atOrAfter(Occurrence(anchor, period, loc, 0)) {
		return Occurrence(anchor, period, loc, 0)
	}

	// Bracket: find hi with occurrence(hi) at/after ref. The period is
	// non-zero (validated in InitDelayDuration), so occurrences strictly
	// increase and the bracket always closes.
	lo, hi := int64(0), int64(1)
	for !atOrAfter(Occurrence(anchor, period, loc, hi)) {
		lo = hi
		hi *= 2
		if hi > maxOccurrenceSearch {
			// Unreachable for validated schedules (period >= 1s and a
			// representable ref); guard against an infinite loop anyway.
			return Occurrence(anchor, period, loc, hi)
		}
	}

	// Binary search the minimal n in (lo, hi] that satisfies the condition.
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		if atOrAfter(Occurrence(anchor, period, loc, mid)) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return Occurrence(anchor, period, loc, hi)
}

// resolveLocalWallClock applies the gap and overlap rules to a time produced
// by time.Date in loc.
//
// Gap (requested wall clock does not exist): time.Date already returns the
// requested instant interpreted with the pre-gap offset, which lands at the
// same offset position after the gap (02:30 -> 03:30). That is the desired
// shift-forward, so the result is kept as is.
//
// Overlap (requested wall clock exists twice): time.Date returns the second
// occurrence; the schedule contract wants the first, so step back while an
// earlier instant has the same local wall clock. DST transitions are never
// larger than two hours in the IANA database, so checking one and two hours
// back is sufficient.
func resolveLocalWallClock(t time.Time, loc *time.Location) time.Time {
	for _, back := range []time.Duration{2 * time.Hour, time.Hour} {
		if earlier := t.Add(-back); sameWallClock(earlier.In(loc), t) {
			t = earlier
		}
	}
	return t
}

// sameWallClock reports whether two times show the same local wall clock
// (date and time of day), regardless of the instant they denote.
func sameWallClock(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute() && a.Second() == b.Second() &&
		a.Nanosecond() == b.Nanosecond()
}

// daysIn returns the number of days in the given month.
func daysIn(year int, month time.Month) int {
	// Day 0 of the next month is the last day of this one.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// unixNanoRepresentable reports whether t is inside the range where
// time.Time.UnixNano is defined (roughly the years 1678 to 2262). Outside
// that range UnixNano silently wraps, which used to corrupt wait-duration
// arithmetic for far-future schedule starts.
func unixNanoRepresentable(t time.Time) bool {
	return time.Unix(0, t.UnixNano()).Unix() == t.Unix()
}
