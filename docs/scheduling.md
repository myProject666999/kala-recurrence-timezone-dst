# Recurrence, timezones and DST

This document defines how Kala computes the run times of a recurring job
(`schedule` of the form `R/<start>/<period>`), what the optional `timezone`
field does, and exactly which behaviors changed relative to the old
("v1") implementation.

The computation lives in `job/schedule.go` as pure functions —
`Occurrence(anchor, period, loc, n)` and
`NextRunTime(anchor, period, loc, ref, inclusive)` — that take the anchor,
the period, a location and a reference time as inputs and never read a clock
or `Job` state. `Job.GetWaitDuration` calls them; `Job.StartWaiting` sets
`NextRunAt` to `now + GetWaitDuration()`, which is exactly the instant the
pure function returned (no rounding in between).

## Calendar units vs. elapsed units

The nth occurrence is always computed as *anchor + n × period*, directly
from the anchor — never by adding the period to the previous (possibly
clamped or delayed) occurrence.

* **Years, months, weeks, days** are *calendar* units: they advance the
  local calendar date and keep the wall clock. `P1D` means "same local time
  tomorrow", so across a DST switch it keeps 09:00 local and the run is 23
  or 25 elapsed hours later.
* **Hours, minutes, seconds** are *elapsed* units: plain time arithmetic.
  `PT24H` is always 24 elapsed hours, so on a switch day it lands one hour
  off the wall clock compared to `P1D`.

Example (Europe/Berlin, spring forward 2024-03-31 02:00→03:00), anchor
`2024-03-30T09:00:00+01:00`:

| period | next run (local)      | next run (UTC)         | elapsed |
|--------|-----------------------|------------------------|---------|
| `P1D`  | 2024-03-31 09:00+02:00 | 2024-03-31T07:00:00Z  | 23 h    |
| `PT24H`| 2024-03-31 10:00+02:00 | 2024-03-31T08:00:00Z  | 24 h    |

On the fall-back day (2024-10-27 03:00→02:00) `P1D` yields
2024-10-27 09:00+01:00 (25 h) and `PT24H` yields 2024-10-27 08:00+01:00
(24 h).

## Month-end clamping

If the anchor's day-of-month does not exist in the target month, the
occurrence lands on the **last day of that month**, and the anchor day is
remembered for later occurrences. A month-by-month comparison with the old
accumulating behavior, anchor January 31, period `P1M`:

| occurrence | old (accumulating `AddDate`) | new (clamped, from anchor) |
|------------|------------------------------|----------------------------|
| 1 (Feb)    | Mar 3 (Jan 31 + 1M normalized) | Feb 28/29 |
| 2 (Mar)    | Apr 3                        | Mar 31   |
| 3 (Apr)    | May 3                        | Apr 30   |
| 4 (May)    | Jun 3                        | May 31   |

The same rule applies to `P1Y` from February 29: Feb 28 in common years,
back to Feb 29 in the next leap year.

## DST gaps and overlaps

When the computed local wall clock falls into a DST transition:

* **Gap (spring forward)** — the local time does not exist. It shifts
  forward to the same offset position after the gap: a daily 02:30 job in
  Europe/Berlin runs at **03:30+02:00** on 2024-03-31 (clocks jumped
  02:00→03:00). This matches `time.Date`'s normalization and is pinned by
  tests.
* **Overlap (fall back)** — the local time occurs twice. The **first**
  occurrence wins: a daily 02:30 job in Europe/Berlin runs at
  **02:30+02:00** (still DST) on 2024-10-27, not at the second 02:30. Note
  `time.Date` alone would pick the second occurrence; the first-occurrence
  rule is applied explicitly and pinned by tests.

## The `timezone` field

`timezone` is an optional IANA name (e.g. `"Europe/Berlin"`) on the job
JSON. It defaults to empty; old JSON and gob records without the field
decode to the empty default on every backend read path (boltdb, redis,
consul, postgres, mysql and mongo all either call `InitDelayDuration` on
load or decode the missing field to `""`).

* **Empty (default)** — legacy behavior: the schedule start's fixed offset
  is used, or UTC when the start has no offset. `next_run_at` keeps its old
  offset/UTC display.
* **Non-empty** —
  * a start *without* an offset (`2017-06-04T19:25:16`) is interpreted as
    wall clock in that timezone;
  * a start *with* an offset must agree with the timezone's offset at that
    instant (`2027-01-15T09:00:00+02:00` with `Europe/Berlin` is rejected,
    because Berlin is at +01:00 in January);
  * an unknown timezone name is rejected at job creation.

Kala does **not** embed a timezone database; it uses the host's IANA tz
data (the `time` package's `LoadLocation`). If the data is missing, job
creation fails with a clear error — it never fails later at run time.
(The test suite skips timezone-dependent tests with an explicit message if
the data is not installed.)

## Validation added in `InitDelayDuration`

* A repeating schedule (`R`, `R<n>` with n ≠ 0) with a **zero-length
  period** (`PT0S`, `P0D`, …) is rejected: it could never advance and used
  to spin the `ResumeAtNextScheduledTime` loop. The one-off form
  `R0/<start>/` is unaffected — its period is never parsed.
* A start **outside the range `time.Time.UnixNano` can represent**
  (years 1678–2262) is rejected with a clear error; it used to wrap the
  wait-duration arithmetic silently.
* The ISO 8601 duration parser is now **anchored**: prefixes/suffixes are
  rejected instead of silently dropped (`XP1D` parsed as `P1D`, `P1W2D` as
  `P1W` before).

## What changed for jobs without a `timezone` — the complete list

Everything not listed here produces the same results as v1.

1. **Month-end drift fixed** (see the table above): occurrences are
   computed from the anchor, so a 31st anchor no longer drifts to the 3rd.
2. **Zero-length repeat period rejected** at creation/load.
3. **Out-of-range start rejected** (years outside 1678–2262).
4. **Duration strings with a prefix or suffix rejected** (anchored regex).
5. **Runs snap to the anchor grid**: the run after the last one is the
   first grid point strictly after the last run, instead of
   `last run + period`. For a last run on the grid this is identical to
   v1; for a last run off the grid (e.g. delayed by jitter) v1 carried the
   offset forward, now the schedule snaps back to the grid. Without a
   `timezone`, the grid's wall clock is interpreted in the last run's
   location — exactly what v1's `Add(lastRun)` did — so DST behavior of
   existing jobs is preserved.

Unchanged on purpose: the first wait of a never-run job whose start passed
(one full interval from `now`), the `Epsilon` retry window (computed via
`RelativeTo`, always elapsed time — no wall-clock semantics), and the
`next_run_at` offset/UTC display for jobs without a `timezone`.

## Fuzzing

`job/schedule_fuzz_test.go` previously skipped two known v1 flaws in
`hitsKnownV1TimingFlaw` (zero-length repeat periods and starts after year
2262). Both are now rejected by `InitDelayDuration`, the skip function is
removed, and the fuzzer covers those inputs directly.
