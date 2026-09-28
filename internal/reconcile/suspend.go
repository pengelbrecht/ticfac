package reconcile

import "time"

// A run's waits and a host that sleeps (epic-6in, 2026-09-28).
//
// epic-6in's close-out waited on CI with a five-minute poll. It logged
// "pending; the close-out is held" at 15:25:07Z and then nothing until
// 15:45:36Z; it logged the re-run of its red go job and "pending" at 15:45:36Z
// and then nothing for twenty-six minutes, while the re-run had gone red at
// 15:56Z and the repair job had real work. It looked hung. It was asleep: the
// Mac entered Idle Sleep at 15:28:49Z and, with dark wakes of seconds, slept
// until 16:09:12Z. Nothing in the run was wrong, and the moment the host woke
// the poll read the second red and dispatched the repair — but only after the
// rest of a sleep that, on macOS, counts the host's AWAKE time: Go's time.Sleep
// runs on a clock that stops while the machine sleeps, so the five-minute
// wait that began at 15:45:36Z fired at 16:11:29Z, two minutes after the wake.
//
// Three answers, one per failure the night showed:
//
//   - the host is held awake while a run lives (the CLI's keep-awake), which
//     is what would have prevented the stall;
//   - a wait sleeps against the WALL clock (sleepByWallClock), so a host that
//     wakes past the poll's time polls at once rather than serving out the
//     awake-time remainder of an interval that already passed;
//   - a wait that took far more wall clock than it asked for SAYS so
//     (StageHostSuspended), so a feed that went silent while the host slept is
//     read as "the host was not running", not as a run that hung.

// wallSlice is the longest single sleep a wall-clock wait takes before it
// looks at the wall clock again: after a host wakes, the poll it owes fires
// within this much awake time.
const wallSlice = 15 * time.Second

// sleepByWallClock waits until d of WALL-clock time has passed, in slices of
// at most wallSlice, so time the host spent suspended counts toward the wait.
// The monotonic reading is stripped (Round(0)) because it is exactly the clock
// that stops while macOS sleeps.
//
// A clock that does not move (a test's fixed now) still ends the wait: the
// slices asked for are counted too, and the wait ends when either reaches d.
func sleepByWallClock(d time.Duration, now func() time.Time, sleep func(time.Duration)) {
	start := now().Round(0)
	var asked time.Duration
	for {
		elapsed := now().Round(0).Sub(start)
		if elapsed < asked {
			elapsed = asked
		}
		if elapsed >= d {
			return
		}
		slice := d - elapsed
		if slice > wallSlice {
			slice = wallSlice
		}
		sleep(slice)
		asked += slice
	}
}

// suspendedSlack is how much longer than it asked a wait may take, in wall
// clock, before the run says the host was suspended: a minute, or the wait's
// own length when that is longer. Scheduling and a loaded host stretch a
// sleep by milliseconds or seconds; a stretch past this is a machine that was
// not running.
func suspendedSlack(d time.Duration) time.Duration {
	if d > time.Minute {
		return d
	}
	return time.Minute
}

// waitSleep is the reconciler's own wait: the configured sleep, and a line in
// the feed when the wall clock says the host was suspended through it.
func (r *Reconciler) waitSleep(sleep func(time.Duration)) func(time.Duration) {
	return func(d time.Duration) {
		before := r.now().Round(0)
		sleep(d)
		took := r.now().Round(0).Sub(before)
		if took < d+suspendedSlack(d) {
			return
		}
		r.record("", StageHostSuspended,
			"the host was suspended for about %s: a %s wait took %s of wall clock, and nothing of this run ran "+
				"in that time — the run did not hang, the machine was not running it (a sleeping laptop, a "+
				"suspended VM). Waits resume against the wall clock; keep the host awake while a run lives",
			(took - d).Round(time.Second), d, took.Round(time.Second))
	}
}
