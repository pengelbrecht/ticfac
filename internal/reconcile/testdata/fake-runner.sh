#!/bin/sh
# The reconciler's fake runner: a worker with the agent taken out.
#
# It is this package's own rather than the executor's, and for one reason that
# matters to a REAL run: every tick writes a file of its own. A fixture in which
# two ticks commit identical content produces an empty second commit, which
# collect reads as `no-commits` — a failure of the fixture that looks exactly
# like a worker that did nothing. The reconciler merges tick after tick onto one
# integration branch, so it is the component that would trip over it.
#
# FAKE_RUNNER_MODE says how it behaves; the prompt arrives as $1 and is ignored.
set -u

mode="${FAKE_RUNNER_MODE:-report}"
status="${FAKE_RUNNER_STATUS:-DONE}"
file="work-${TICFAC_TICK}.txt"

commit() {
	printf 'tick %s, attempt %s, mode %s\n' "$TICFAC_TICK" "$TICFAC_ATTEMPT" "$mode" > "$TICFAC_WORKTREE/$file"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: ${TICFAC_TICK}" >/dev/null 2>&1
}

report() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner ran in mode %s.\n\n' "$mode"
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

case "$mode" in
report)
	commit
	report
	;;
silent)
	# Settled and incomplete: work committed, nothing said.
	commit
	;;
nocommit)
	report
	;;
blocked-first)
	# Attempt 1 is a worker that found a blocker of its own still open and said
	# so: a report with STATUS: BLOCKED, and no commit at all. Every later
	# attempt is the same worker dispatched again after that blocker closed.
	if [ "$TICFAC_ATTEMPT" = "1" ]; then
		status=BLOCKED
		report
	else
		commit
		report
	fi
	;;
boundary)
	# A record under the tracker's authority, at a path the RECONCILER's own
	# tracker does not write. The file the worker would really forge is
	# `.tick/issues/$TICFAC_TICK.json`, and the reconciler writes that one as it
	# claims and closes the tick — so a fixture that forged the same path would
	# be refused by a merge conflict, and the negative control (the guard off,
	# the write reaching origin unnoticed) could never be observed at all.
	mkdir -p "$TICFAC_WORKTREE/.tick/issues"
	printf '{"id":"forged-%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/forged-$TICFAC_TICK.json"
	commit
	report
	;;
hang)
	# Commits, then never finishes: the shape of a worker that is killed.
	# `exec` replaces this shell with the sleeper, so the runner's process
	# group has exactly one member for a group kill to reach, never a
	# transient window with a forked child a group signal could miss.
	commit
	exec sleep 86400
	;;
*)
	printf 'unknown FAKE_RUNNER_MODE %s\n' "$mode" >&2
	exit 64
	;;
esac
