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

verdict_line() {
	# The review's typed verdict line (tick b50): only the review-epic role is
	# asked for one, so only that role writes one. It sits before the final
	# status line, which stays the report's last line.
	if [ "$TICFAC_ROLE" = "review-epic" ]; then
		printf 'REVIEW-VERDICT: %s\n' "${FAKE_RUNNER_REVIEW_VERDICT:-READY}"
	fi
}

report() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner ran in mode %s.\n\n' "$mode"
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

findings_block() {
	# The findings channel (tick 7vn): the typed block a worker reports its
	# discoveries in. Two findings, deliberately different in target: one for
	# the repository being run, one routed upstream — a worker that discovers
	# something on ANOTHER tracker names which one, and the draft keeps it.
	# Since tick nfo the block also carries the first finding's DONE EVIDENCE
	# (done_item + demonstrating_check), and the second finding deliberately
	# carries none: a linked finding and an unlinked one are both shapes the
	# real worker produces, and the drafts must keep both claims honestly.
	{
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "A finding the fake runner proposes",'
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "done_item": "A1",'
		printf '%s\n' '  "demonstrating_check": "go"'
		printf '%s\n' '}, {'
		printf '%s\n' '  "kind": "upstream-tick",'
		printf '%s\n' '  "title": "An upstream finding routed to another repository",'
		printf '%s\n' '  "body": "",'
		printf '%s\n' '  "severity": "low",'
		printf '%s\n' '  "target": "pengelbrecht/ticks"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
	}
}

findings_block_local() {
	# The absorption fixture (tick npq): ONE finding, for the repository being
	# run, claiming done item A1 — the shape a worker's report carries when the
	# discovery is this epic's own ground. The upstream half of the two-finding
	# block is deliberately absent: a routed finding stays a person's at the
	# close-out, and a test that drives an absorption all the way to a completed
	# run needs nothing holding the hand-over.
	{
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "A finding the fake runner proposes",'
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "done_item": "A1",'
		printf '%s\n' '  "demonstrating_check": "done"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
	}
}

report_with_findings() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found things outside its tick.\n\n'
		findings_block
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

# A review that judges the epic NOT READY the way the review-epic contract
# asks since epic-6in: detail on the verdict line ($1), and the blocking
# finding as a high-severity finding ($2) beside a low one that is not a
# reason and stays backlog.
review_not_ready_report() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The review judged the epic not ready.\n\n'
		printf '%s\n' '```findings v2'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "defect",'
		printf '%s\n' "  \"title\": \"$2\","
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "body": "The reason the epic is not ready."'
		printf '%s\n' '}, {'
		printf '%s\n' '  "kind": "proposal",'
		printf '%s\n' '  "title": "A polish the review noticed on the way",'
		printf '%s\n' '  "severity": "low"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
		printf '\n'
		printf 'REVIEW-VERDICT: NOT READY — %s\n' "$1"
		printf 'STATUS: DONE_WITH_CONCERNS\n'
	} > "$TICFAC_RESULT_PATH"
}

# The gate-repair worker (tick wj6): the gate failed because a deletion left
# a stale reference — a check that reads a file the tick deleted. The fake
# stands in for an agent that read the failing check's output out of the
# evidence record its inputs name, read the tick's own diff, and made the
# same small mechanical fix a person made by hand twice on epic-yoh: the
# harness stops referencing the deleted file.
repair_gate_failure() {
	printf 'cat README.md >/dev/null\n' > "$TICFAC_WORKTREE/check.sh"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "repair: the harness stops referencing the deleted file" >/dev/null 2>&1
}

# The repair that lands work but fixes nothing: its merge is gated again,
# fails again, and that is the stop that names BOTH failures — the bound on
# how much a run repairs by itself.
repair_gate_nothing() {
	printf 'a repair that did not fix the gate\n' > "$TICFAC_WORKTREE/repair-note.txt"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "repair: a change that fixes nothing" >/dev/null 2>&1
}

# One side of the wj6 fixture (tick 2p6's conflict_side shape, single-sided):
# the tick $GATE_BREAK_TICK names deletes a file the gate's own check still
# references — a deletion whose dependent lives OUTSIDE the files the tick
# declared — so whichever merge lands it fails the integrated gate with the
# check's own output naming the missing file.
gate_break_side() {
	rm -f "$TICFAC_WORKTREE/stale-ref.txt"
	commit
	report
}

in_gate_break_tick() {
	case " ${GATE_BREAK_TICK:-a1} " in *" $TICFAC_TICK "*) return 0 ;; esac
	return 1
}

findings_block_chain() {
	# The recursion fixture (tick qjj): ONE finding whose identity is the tick
	# that reported it, so every attempt of every tick discovers a NEW defect
	# — an absorbed tick's own work reports the next link of an absorption
	# chain rather than deduplicating against the finding that created it.
	# Like finding_local it claims done item A1, so against the observed gate
	# every link of the chain is judged gating and the chain grows until the
	# bound stops it.
	{
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' "  \"title\": \"A finding the fake runner reports from $TICFAC_TICK\","
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "done_item": "A1",'
		printf '%s\n' '  "demonstrating_check": "done"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
	}
}

report_with_chained_findings() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found things outside its tick.\n\n'
		findings_block_chain
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

findings_block_routed() {
	# The epic-2jn close-out stall (2026-09-27): ONE finding routed to ANOTHER
	# repository that nonetheless claims to break a done item of this epic.
	# This run can never fix another repository, so the claim cannot make it
	# gate anything here, and the run must dispose of it with nobody triaging.
	{
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "upstream-tick",'
		printf '%s\n' '  "title": "An upstream finding routed to another repository",'
		printf '%s\n' '  "body": "The upstream half, reported verbatim.",'
		printf '%s\n' '  "severity": "low",'
		printf '%s\n' '  "target": "pengelbrecht/ticks",'
		printf '%s\n' '  "done_item": "A1",'
		printf '%s\n' '  "demonstrating_check": "none"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
	}
}

report_with_routed_findings() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found things outside its tick.\n\n'
		findings_block_routed
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

report_with_local_findings() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found things outside its tick.\n\n'
		findings_block_local
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

# The resolve-conflict worker (tick 2p6): every file that still carries
# git's conflict markers is rewritten as the resolved union — the fake
# stands in for an agent that read both ticks' records and made the tree
# both intents live in. The markers are the fixture's own: the reconciler
# hands the job a worktree cut at the conflicted merge itself.
resolve_union() {
	for f in $(grep -rl '^<<<<<<<' "$TICFAC_WORKTREE" --exclude-dir=.git 2>/dev/null); do
		printf 'resolved by the resolve-conflict job\n' > "$f"
	done
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "resolve-conflict: $TICFAC_TICK" >/dev/null 2>&1
}

# One side of a CONTENT conflict between two same-wave ticks (tick 2p6): the
# ticks $CONFLICT_TICKS names both edit one file that exists at the base, so
# whichever of them merges second meets a real content conflict.
#
# The two workers SYNC before either commits, so both branch from one epic
# head; the SECOND side then waits out the first's merge, so the conflict is
# deterministic — the first tick merges cleanly, the second conflicts. The
# wait is on the other side's .started marker (an event), bounded so a run
# that never makes the other dispatch still ends and fails the test on its
# assertion rather than on a timeout.
conflict_side() {
	mkdir -p "${CONFLICT_SYNC:?the conflict fixture needs CONFLICT_SYNC}"
	: > "$CONFLICT_SYNC/$TICFAC_TICK.started"
	set -- ${CONFLICT_TICKS:?the conflict fixture needs CONFLICT_TICKS}
	first="$1"
	needed=$(printf '%s\n' $CONFLICT_TICKS | grep -c .)
	waited=0
	while [ "$(ls "$CONFLICT_SYNC" 2>/dev/null | grep -c '\.started$')" -lt "$needed" ] && [ "$waited" -lt 60 ]; do
		sleep 1
		waited=$((waited + 1))
	done
	if [ "$TICFAC_TICK" != "$first" ]; then
		sleep 2
	fi
	printf 'the %s side of the shared file\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/shared-work.txt"
	commit
	report
	# A worker that writes its report and then keeps going for a moment — it
	# tidies up, or its supervisor is still pushing — before it exits (hol).
	# $CONFLICT_LINGER holds the side that conflicts that long past its
	# report, so the run collects it, and releases it, while it is still
	# alive.
	if [ "$TICFAC_TICK" != "$first" ] && [ -n "${CONFLICT_LINGER:-}" ]; then
		sleep "$CONFLICT_LINGER"
	fi
}

in_conflict_ticks() {
	case " ${CONFLICT_TICKS:-} " in *" $TICFAC_TICK "*) return 0 ;; esac
	return 1
}

# A runner that exited 0 without its report is re-prompted by its supervisor
# (subprocess/nudge.go), with TICFAC_NUDGE set. Every mode here that ends
# without a report MEANT to — it is how the fixture makes a missing result —
# so a nudged re-run says nothing more, and only the modes written for the
# nudge answer it.
if [ -n "${TICFAC_NUDGE:-}" ]; then
	case "$mode" in
	conflict_resolve_stops_early) ;;
	*) exit 0 ;;
	esac
fi

case "$mode" in
report)
	commit
	report
	;;
conflict)
	# Two same-wave ticks rewrite one shared file (tick 2p6): whichever
	# merges second hits a real content conflict, and the run's answer is
	# the resolve-conflict job — dispatched by the reconciler with role
	# resolve-conflict, whose fake worker makes the union. Every other
	# tick behaves like the plain report mode.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		resolve_union
		report
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
conflict_resolve_while_live)
	# Epic hn6, run_6d88e3de: the same conflict, and a THIRD tick ($LIVE_TICK)
	# whose worker is still running when the resolve-conflict job starts. It
	# finishes only once the resolve has started; the resolve answers only
	# once it has, and a moment after — so whether the run addressed the third
	# tick DURING the resolve is read straight off the event order.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		: > "$CONFLICT_SYNC/resolve.started"
		waited=0
		while [ ! -e "$CONFLICT_SYNC/live.done" ] && [ "$waited" -lt 600 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		sleep 1
		resolve_union
		report
	elif in_conflict_ticks; then
		conflict_side
	elif [ "$TICFAC_TICK" = "${LIVE_TICK:-}" ]; then
		waited=0
		while [ ! -e "$CONFLICT_SYNC/resolve.started" ] && [ "$waited" -lt 600 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		commit
		report
		: > "$CONFLICT_SYNC/live.done"
	else
		commit
		report
	fi
	;;
conflict_resolve_hold)
	# epic-2jn (4mv attempt 33): the same conflict, and a resolve-conflict
	# worker that writes its resolution into its worktree WITHOUT committing
	# it and then waits — through the orchestrator's SIGTERM, which a local
	# worker is a separate process from and survives — until $EVAC_GO
	# appears. Only then does it commit and settle. Every start is counted in
	# $CONFLICT_SYNC/resolve.starts, and $CONFLICT_SYNC/resolve.holding says
	# the uncommitted resolution is in the tree.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		printf '%s\n' "$$" >> "$CONFLICT_SYNC/resolve.starts"
		for f in $(grep -rl '^<<<<<<<' "$TICFAC_WORKTREE" --exclude-dir=.git 2>/dev/null); do
			printf 'resolved by the resolve-conflict job\n' > "$f"
		done
		: > "$CONFLICT_SYNC/resolve.holding"
		waited=0
		while [ ! -e "${EVAC_GO:-/nonexistent}" ] && [ "$waited" -lt 1200 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" commit -q -m "resolve-conflict: $TICFAC_TICK" >/dev/null 2>&1
		report
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
conflict_resolve_stops_early)
	# epic-2jn vqc (2026-09-27): the same conflict, and a resolve-conflict
	# worker that commits its resolution, starts the gate "in the
	# background" and ends its turn to wait for it — which in print mode
	# exits 0 with no report. Re-prompted, it writes the report.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		if [ -z "${TICFAC_NUDGE:-}" ]; then
			resolve_union
			exit 0
		fi
		report
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
conflict_unresolvable)
	# The same conflict, and a resolve job that cannot resolve it: it
	# answers BLOCKED over an empty branch — a resolve that asks for a
	# person, which is the stop the acceptance still names the files for.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		status=BLOCKED
		report
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
gate_break_repair)
	# The wj6 shape: a tick deletes a file the gate's own check still
	# references (a deletion whose dependent lives outside the files the
	# tick declared), so the merge lands and the integrated gate fails with
	# the check's own output naming the missing file. The run's answer is the
	# plan-repair job — dispatched at the policy's ceiling tier, with the
	# failing check's evidence record in its inputs — whose fake worker makes
	# the small mechanical fix and reports; the merge is gated again and the
	# tick closes behind a passing gate without a person.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		repair_gate_failure
		report
	elif in_gate_break_tick; then
		gate_break_side
	else
		commit
		report
	fi
	;;
conflict_resolve_noreport)
	# epic-2jn (vqc attempt 50): the same conflict, and a resolve-conflict
	# worker whose FIRST start commits a clean resolution and then exits 0
	# without writing a report — the runner that ended its turn waiting on a
	# background task. Every later start makes the union again (a worktree cut
	# at the committed resolution has no markers left, so it commits nothing)
	# and reports. Every start is counted in $CONFLICT_SYNC/resolve.starts.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		printf '%s\n' "$TICFAC_JOB_ID" >> "$CONFLICT_SYNC/resolve.starts"
		resolve_union
		if [ "$(grep -c . "$CONFLICT_SYNC/resolve.starts")" -gt 1 ]; then
			report
		fi
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
conflict_resolve_silent)
	# The same conflict, and a resolve-conflict worker that NEVER reports:
	# every start makes the union and exits 0 without a report. Starts are
	# counted in $CONFLICT_SYNC/resolve.starts.
	if [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		printf '%s\n' "$TICFAC_JOB_ID" >> "$CONFLICT_SYNC/resolve.starts"
		resolve_union
	elif in_conflict_ticks; then
		conflict_side
	else
		commit
		report
	fi
	;;
gate_break_repair_noreport)
	# The wj6 gate failure, and a plan-repair worker whose FIRST start commits
	# the fix and exits 0 without a report; every later start makes the fix
	# again (nothing left to change when it is cut at the committed fix) and
	# reports. Starts are counted in $REPAIR_SYNC/repair.starts.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		printf '%s\n' "$TICFAC_JOB_ID" >> "${REPAIR_SYNC:?}/repair.starts"
		repair_gate_failure
		if [ "$(grep -c . "$REPAIR_SYNC/repair.starts")" -gt 1 ]; then
			report
		fi
	elif in_gate_break_tick; then
		gate_break_side
	else
		commit
		report
	fi
	;;
gate_break_unresolvable)
	# The same gate failure, and a repair job that cannot fix it: it answers
	# BLOCKED over an empty branch — a repair that asks for a person, which is
	# the stop the acceptance still names both the gate and the repair for.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		status=BLOCKED
		report
	elif in_gate_break_tick; then
		gate_break_side
	else
		commit
		report
	fi
	;;
gate_break_wrong_repair)
	# The repair that lands work but fixes nothing: its merge is merged and
	# gated as usual, the gate fails again over the repaired tree, and that is
	# the stop naming BOTH failures — one repair per tick is all a run
	# dispatches.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		repair_gate_nothing
		report
	elif in_gate_break_tick; then
		gate_break_side
	else
		commit
		report
	fi
	;;
stall-then-report)
	# The Phase 3 shape (tick 7zs), with an ending: the worker is alive,
	# produces nothing — no commit, no file, no report — for long enough that
	# the run's stall warning must fire, and then does its work and settles
	# DONE. The stall warning must not change what the run concludes about an
	# attempt that was merely slow to start.
	sleep 1
	commit
	report
	;;
linger-until)
	# g50's shape: one worker keeps thinking while another tick's attempt
	# settles and closes, so that whatever the run dispatches next is
	# dispatched BESIDE a live attempt or not at all. $LINGER_TICK names the
	# worker that lingers and $LINGER_UNTIL the file the test touches when the
	# dispatch it is watching for happens.
	#
	# It waits on the event rather than on a duration: a sleep long enough to
	# be safe is a slow test, and one short enough to be fast is a flaky one.
	# The bound is only so that a run which never makes that dispatch still
	# ends, and fails the test on its assertion rather than on a timeout.
	if [ "$TICFAC_TICK" = "${LINGER_TICK:-}" ] && [ -n "${LINGER_UNTIL:-}" ]; then
		waited=0
		while [ ! -e "$LINGER_UNTIL" ] && [ "$waited" -lt 60 ]; do
			sleep 1
			waited=$((waited + 1))
		done
	fi
	# $BOUNDARY_TICK, when set, names a tick whose every try also writes the
	# boundary mode's forged tracker record beside its work: a refusal on the
	# merits that lands while the lingering worker is still thinking.
	if [ "$TICFAC_TICK" = "${BOUNDARY_TICK:-}" ]; then
		mkdir -p "$TICFAC_WORKTREE/.tick/issues"
		printf '{"id":"forged-%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/forged-$TICFAC_TICK.json"
	fi
	# $VANISH_TICK, when set, names a tick whose first $VANISH_TRIES tries die
	# without a commit or a report (a worker that never answered: the
	# missing-result a lost container leaves), counted in the file
	# $VANISH_COUNT; its later tries do the work.
	if [ "$TICFAC_TICK" = "${VANISH_TICK:-}" ]; then
		tries=$(cat "${VANISH_COUNT:?}" 2>/dev/null || echo 0)
		if [ "$tries" -lt "${VANISH_TRIES:-1}" ]; then
			echo $((tries + 1)) > "$VANISH_COUNT"
			exit 0
		fi
	fi
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
tracker_edit | tracker_edit_drop)
	# The yjq shape (hn6 run_d51a): b1's deliverable IS a tracker edit — the
	# epic's acceptance re-flowed one [A<n>] item per line — which no worker
	# may write. b1 commits nothing and PROPOSES the edit in a typed block;
	# every other tick does its work (b1 is the second wave's, so the gate's
	# tree already carries the first wave's work when it runs over b1's
	# delivery). tracker_edit_drop has b1 propose an edit that drops an item
	# the record marks, which the run must refuse.
	if [ "$TICFAC_TICK" != "b1" ]; then
		commit
		report
	else
		value='[A1] Every tick closes behind a green gate;\n[A2] The fixture epic carries a second item.'
		if [ "$mode" = "tracker_edit_drop" ]; then
			value='[A1] Every tick closes behind a green gate.'
		fi
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf 'The fix is the epic record itself; proposed for the run to apply.\n\n'
			printf '%s\n' '```tracker-edits'
			printf '[{"tick": "qeu", "field": "acceptance_criteria", "value": "%s"},\n' "$value"
			printf '%s\n' ' {"tick": "b1", "field": "notes", "value": "re-flowed the epic acceptance one item per line"}]'
			printf '%s\n' '```'
			printf '\n%s\n%s\n%s\n\n' '```findings v2' '[]' '```'
			printf 'STATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	fi
	;;
tracker_edit_blocked | tracker_edit_blocked_drop | tracker_edit_blocked_ask)
	# The blocked shape this tick answers (hn6 l89): b1's deliverable IS a
	# tracker edit, the boundary forbids a worker writing it, and the worker
	# answers BLOCKED with the exact edit proposed in the typed block — the
	# yjq shape. The run applies the proposal itself and closes the tick,
	# instead of dispatching the question up the blocked ladder.
	# tracker_edit_blocked_drop has the blocked answer propose an edit that
	# drops an item the record marks: the run refuses it, and the ladder —
	# not the apply — answers the question. tracker_edit_blocked_ask names a
	# question the standing orders reserve for a person (credentials) beside
	# a named edit that is valid: the question still holds, and no edit is
	# written — an always-ask question is a person's whatever else the
	# report carries. Every try of the ask mode answers the same, so the
	# hold is the only place it can end.
	if [ "$TICFAC_TICK" != "b1" ]; then
		commit
		report
	elif [ "$mode" != "tracker_edit_blocked_ask" ] && [ "$TICFAC_TRY" != "1" ]; then
		# A later try: the ladder dispatched it with the question (only the
		# refused shape reaches one), and the worker does its work.
		commit
		report
	else
		value='[A1] Every tick closes behind a green gate;\n[A2] The fixture epic carries a second item.'
		detail='the fix is the epic record itself, which the boundary does not let me write; the edit is proposed above for the run to apply'
		if [ "$mode" = "tracker_edit_blocked_drop" ]; then
			value='[A1] Every tick closes behind a green gate.'
		fi
		if [ "$mode" = "tracker_edit_blocked_ask" ]; then
			detail='the migration needs the production credential nobody gave me; the epic acceptance re-flow above is ready to apply'
		fi
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf 'The fix is the epic record itself; proposed for the run to apply.\n\n'
			printf '%s\n' '```tracker-edits'
			printf '[{"tick": "qeu", "field": "acceptance_criteria", "value": "%s"},\n' "$value"
			printf '%s\n' ' {"tick": "b1", "field": "notes", "value": "re-flowed the epic acceptance one item per line"}]'
			printf '%s\n' '```'
			printf '\n%s\n%s\n%s\n\n' '```findings v2' '[]' '```'
			printf 'STATUS: BLOCKED — %s\n' "$detail"
		} > "$TICFAC_RESULT_PATH"
	fi
	;;
tracker_edit_finding)
	# The triage half: a1 does its own work and reports a finding whose
	# whole fix is a tracker edit, with the exact change — the run applies
	# it rather than absorbing a tick no worker could do. Every other tick
	# does its work.
	commit
	if [ "$TICFAC_TICK" != "a1" ]; then
		report
	else
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf '%s\n' '```findings v2'
			printf '%s\n' '[{"kind": "defect", "title": "The epic acceptance parses as one item", "severity": "high",'
			printf '%s\n' '  "breaks": {"item": "A1"},'
			printf '%s\n' '  "tracker_edit": {"tick": "qeu", "field": "acceptance_criteria", "value": "[A1] Every tick closes behind a green gate;\n[A2] The fixture epic carries a second item."}}]'
			printf '%s\n' '```'
			printf '\nSTATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	fi
	;;
a1-adds-nothing)
	# The isp shape: a1's attempt was cut from a RELEASED attempt's carried
	# work, finds the work already done, and correctly adds nothing — a
	# report over an empty branch. Every other tick does its work, so the
	# run's only question is what the carried attempt delivers.
	# FAKE_RUNNER_A1_STATUS is a1's status alone (tick tyd): a question a
	# person must answer is a1's here, and the other ticks keep going.
	if [ "$TICFAC_TICK" != "a1" ]; then
		commit
	else
		status="${FAKE_RUNNER_A1_STATUS:-$status}"
	fi
	report
	;;
finding)
	# The discovery case: the work is done, the report is DONE, and the
	# report also carries a typed findings block — one finding for this
	# repository and one routed upstream.
	commit
	report_with_findings
	;;
finding_local)
	# The absorption case (tick npq): the work is done, the report is DONE, and
	# the report carries ONE in-repository finding claiming done item A1 — the
	# discovery that the absorption decision is driven on.
	commit
	report_with_local_findings
	;;
finding_routed)
	# The routed case (the epic-2jn close-out stall): the work is done, the
	# report is DONE, and the report carries ONE finding routed to another
	# repository, claiming a done item of this epic it can never gate.
	commit
	report_with_routed_findings
	;;
finding_chain)
	# The recursion case (tick qjj): same as finding_local, but the finding's
	# identity is the reporting tick, so every attempt discovers a NEW defect
	# and an absorbed tick's own work grows the chain. The run's answer is the
	# depth bound: a chain at the bound defers the next link to a backlog tick
	# with an owner, carrying the chain, and the run carries on.
	commit
	report_with_chained_findings
	;;
review_finding)
	# The 604 shape: only the review job reports findings — an upstream
	# discovery made by a read-only role job whose deliverable is its answer.
	# Everything else is the plain report mode.
	if [ "$TICFAC_TICK" = "rv" ]; then
		report_with_findings
	else
		commit
		report
	fi
	;;
closeout_nocommit)
	# The pwp shape (tick 19l): the close-out answers DONE_WITH_CONCERNS over
	# an EMPTY branch — no commits at all — while every other tick does its
	# work and reports. The role's own status and the run's verdict are then
	# two different answers to two different questions, and the feed line has
	# to say both without attributing the verdict to the worker.
	if [ "$TICFAC_TICK" = "co" ]; then
		status="DONE_WITH_CONCERNS"
		report
	else
		commit
		report
	fi
	;;
review_not_ready)
	# The pxc shape (tick b50): the review judges the epic NOT READY — DONE_WITH_
	# CONCERNS in the status vocabulary, its own typed line stating the verdict —
	# over the empty branch a correct read-only review leaves. The run must
	# record that answer as its own verdict, never as the collect vocabulary's
	# ready-to-merge, and carry it to the epic PR. Since epic-6in the NOT READY
	# names a blocking (high) finding, which the run absorbs and fixes before
	# reviewing again — and in THIS mode the re-review is NOT READY too, so the
	# bound on review rounds is what is left. Every other tick is the plain
	# report mode.
	if [ "$TICFAC_ROLE" = "review-epic" ] && [ "$TICFAC_TICK" = "rv" ]; then
		review_not_ready_report "the Phase 4 gate run never happened" "The Phase 4 gate run never happened"
	elif [ "$TICFAC_ROLE" = "review-epic" ]; then
		review_not_ready_report "the Phase 4 gate still never ran" "The Phase 4 gate still never ran after the fix"
	else
		commit
		report
	fi
	;;
review_not_ready_then_ready)
	# The epic-6in fix: the first review judges the epic NOT READY naming one
	# blocking (high) finding and one low one; the run absorbs the blocking one
	# into the epic, works it (the plain report mode), and reviews again — and
	# the re-review answers READY.
	if [ "$TICFAC_ROLE" = "review-epic" ] && [ "$TICFAC_TICK" = "rv" ]; then
		review_not_ready_report "the Phase 4 gate run never happened" "The Phase 4 gate run never happened"
	elif [ "$TICFAC_ROLE" = "review-epic" ]; then
		report
	else
		commit
		report
	fi
	;;
review_no_verdict)
	# The refusal case (tick b50): a review whose report says in prose that the
	# epic is not ready but never states its typed REVIEW-VERDICT line. Prose is
	# where NOT READY went to be recorded as its opposite, so the answer is
	# refused rather than guessed at — the run fails naming the line the report
	# lacked, and the review tick is not closed behind a judgement nobody can
	# read. Every other tick is the plain report mode.
	if [ "$TICFAC_TICK" = "rv" ]; then
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf 'The review says in prose that the epic is not ready, but the typed line is missing.\n\n'
			printf 'STATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	else
		commit
		report
	fi
	;;
closeout_finding)
	# The 4sb shape: the close-out's OWN attempt reports a finding that did
	# not exist when the admission composed the PR body — the fact that makes
	# the close gate REWRITE the body from the final records rather than trust
	# the admission's view. Only the close-out reports; every other tick is the
	# plain report mode, so the finding on the PR is provably the close-out's.
	if [ "$TICFAC_TICK" = "co" ]; then
		commit
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf 'The close-out also found something outside its tick.\n\n'
			printf '%s\n' '```findings'
			printf '%s\n' '[{'
			printf '%s\n' '  "kind": "proposed-tick",'
			printf '%s\n' '  "title": "A finding the close-out itself proposes",'
			printf '%s\n' '  "body": "Found by the close-out, after the admission wrote the PR body.",'
			printf '%s\n' '  "severity": "medium",'
			printf '%s\n' '  "target": ""'
			printf '%s\n' '}]'
			printf '%s\n' '```'
			printf '\nSTATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	else
		commit
		report
	fi
	;;
finding_bad)
	# A findings block that does not parse: collect carries the problem, and
	# the reconciler refuses the attempt rather than closing the tick behind
	# findings nobody could read.
	commit
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf '%s\n' '```findings'
		printf '%s\n' '[{"kind": "defect", "title": "a block that never ends",'
		printf '%s\n' '```'
		printf '\nSTATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
finding_blocked)
	# The v3i shape: the FIRST TRY of a1 finds a blocker of its own and
	# answers BLOCKED with nothing committed — but its report also carries a
	# typed finding, so the discovery is drafted even though the attempt is
	# refused. Every later try of a1 commits, answers DONE, and reports the
	# SAME finding: the dedup case, a finding that recurs until it is fixed.
	# Other ticks behave like the plain report mode.
	#
	# The gate is $TICFAC_TRY — the tick's OWN try count — never
	# $TICFAC_ATTEMPT (tick vw0): attempt numbers count the RUN's dispatches,
	# so a test that dispatches another tick first moves a1's first try off
	# the run-wide number 1, and a mode keyed on that number would hand a1
	# DONE where the fixture means BLOCKED.
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_TRY" = "1" ]; then
		status=BLOCKED
		report_with_findings
	elif [ "$TICFAC_TICK" = "a1" ]; then
		commit
		report_with_findings
	else
		commit
		report
	fi
	;;
finding_folds)
	# The fold case (tick ryv): a1's report carries a finding with a key the
	# record does not know — the 3h0 shape, an extra "title_note" — over
	# otherwise DONE work. The attempt must be ACCEPTED, not refused as
	# finding_report_invalid: the finding is drafted with the key folded into
	# its body as a labelled line, the run's records note the fold, and the
	# tick closes as it would for any other discovery. Every other tick is
	# the plain report mode.
	if [ "$TICFAC_TICK" = "a1" ]; then
		commit
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf 'The fake runner also found things outside its tick.\n\n'
			printf '%s\n' '```findings'
			printf '%s\n' '[{'
			printf '%s\n' '  "kind": "proposed-tick",'
			printf '%s\n' '  "title": "A finding carrying an extra key",'
			printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
			printf '%s\n' '  "severity": "medium",'
			printf '%s\n' '  "target": "",'
			printf '%s\n' '  "title_note": "an annotation the record has no field for"'
			printf '%s\n' '}]'
			printf '%s\n' '```'
			printf '\n'
			verdict_line
			printf 'STATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	else
		commit
		report
	fi
	;;
blocked-first)
	# A tick's FIRST TRY is a worker that found a blocker of its own still open
	# and said so: a report with STATUS: BLOCKED, and no commit at all. Every
	# later try is the same worker dispatched again after that blocker closed.
	#
	# The gate is $TICFAC_TRY — the tick's OWN try count — never
	# $TICFAC_ATTEMPT (tick vw0): attempt numbers count the RUN's dispatches,
	# so the tick dispatched second blocks on the run-wide number 2, not 1,
	# and a mode keyed on the run-wide number would let its first try quietly
	# answer DONE.
	if [ "$TICFAC_TRY" = "1" ]; then
		status=BLOCKED
		report
	else
		commit
		report
	fi
	;;
ask)
	# A worker that stops to ask (tick tyd): a1 (or $FAKE_RUNNER_ASK_TICK)
	# commits and answers BLOCKED with $FAKE_RUNNER_QUESTION until its prompt
	# answers it — at once when it
	# is re-dispatched with the question (FAKE_RUNNER_ASK_UNTIL=escalated), or
	# only once the prompt tells it to decide under the STANDING ORDERS (the
	# default, "decide"; "never" asks every time). A worker that decides logs
	# the decision under a Decisions heading, as the prompt asks. Other ticks
	# behave like the plain report mode. The prompt arrives as $1.
	prompt="${1:-}"
	until="${FAKE_RUNNER_ASK_UNTIL:-decide}"
	answered=no
	if printf '%s' "$prompt" | grep -q "An earlier attempt stopped to ask"; then
		if [ "$until" = "escalated" ]; then
			answered=yes
		elif [ "$until" = "decide" ] && printf '%s' "$prompt" | grep -q "STANDING ORDERS"; then
			answered=yes
		fi
	fi
	# FAKE_RUNNER_ASK_COMMIT=no: the asking worker commits nothing before it
	# asks (the no-commits shape); once answered it commits like any other.
	if [ "${FAKE_RUNNER_ASK_COMMIT:-yes}" != "no" ] || [ "$answered" = "yes" ] ||
		[ "$TICFAC_TICK" != "${FAKE_RUNNER_ASK_TICK:-a1}" ]; then
		commit
	fi
	if [ "$TICFAC_TICK" != "${FAKE_RUNNER_ASK_TICK:-a1}" ]; then
		report
	elif [ "$answered" = "yes" ]; then
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf '## Decisions\n\n'
			printf -- '- question: %s; choice: the first option; reason: the standing orders delegate it; class: naming\n\n' "${FAKE_RUNNER_QUESTION:-}"
			verdict_line
			printf 'STATUS: DONE\n'
		} > "$TICFAC_RESULT_PATH"
	else
		status="BLOCKED — ${FAKE_RUNNER_QUESTION:-a question}"
		report
	fi
	;;
empty-first)
	# A genuine collect failure (tick tyd's follow-up): a tick's FIRST TRY
	# says it is done with concerns and commits nothing, so the collect is
	# `no-commits` and the attempt is spent — collect_failed, a stop, and a
	# redispatch on resume. It is NOT a question: since tyd a worker that
	# answers BLOCKED is answered in-run, so the resume-and-redispatch path
	# needs a worker that failed without asking. Every later try commits.
	# Keyed on $TICFAC_TRY, the tick's own try, like blocked-first.
	if [ "$TICFAC_TRY" = "1" ]; then
		status="DONE_WITH_CONCERNS — the change needed nothing committed"
		report
	else
		commit
		report
	fi
	;;
blocked-with-work)
	# The escalation the fixtures never had: a worker that DID the work, committed
	# it, and still ends its report with STATUS: BLOCKED — it found something only
	# a person can settle and said so. `blocked-first` reports BLOCKED with no
	# commit, so every check that only ever asked "is there a commit?" passed it
	# for the wrong reason; this mode is the same escalation with a branch that
	# looks perfectly mergeable.
	commit
	status="BLOCKED — the migration needs a production credential nobody gave me"
	report
	;;
unpushed-tail)
	# The shape a supervisor whose FINAL push failed leaves behind: origin
	# carries an EARLIER commit of this attempt and the branch carries a later
	# one. The first push is made here, by hand, so the divergence exists
	# whatever the supervisor's timer did; the second commit is never pushed.
	commit
	git -C "$TICFAC_WORKTREE" push -q origin "HEAD:refs/heads/$TICFAC_BRANCH" >/dev/null 2>&1
	printf 'the commit that never reached the remote\n' > "$TICFAC_WORKTREE/late-${TICFAC_TICK}.txt"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: late ${TICFAC_TICK}" >/dev/null 2>&1
	report
	;;
forge-base)
	# The boundary check, measured from a base the enforced party chose. The
	# attempt writes a tracker record under an authority that is not its own,
	# then rewrites the base_sha in its own attempt record — which sits beside
	# its worktree, under its own uid — to the commit that carries it. Every
	# diff after that starts AFTER the violation, so the executor sees a clean
	# attempt. Only the reconciler's marker on origin still knows the base.
	mkdir -p "$TICFAC_WORKTREE/.tick/issues"
	printf '{"id":"forged-%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/forged-$TICFAC_TICK.json"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: forged ${TICFAC_TICK}" >/dev/null 2>&1
	forged=$(git -C "$TICFAC_WORKTREE" rev-parse HEAD)
	commit
	state=$(dirname "$TICFAC_PROMPT_FILE")
	sed -e "s/\"base_sha\": \"[0-9a-f]*\"/\"base_sha\": \"$forged\"/" \
		-e "s/\"base_sha\":\"[0-9a-f]*\"/\"base_sha\":\"$forged\"/" \
		"$state/attempt.json" > "$state/attempt.json.new"
	mv "$state/attempt.json.new" "$state/attempt.json"
	report
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
push-per-tick)
	# Every worker tries to advance a ref of its OWN on origin, twice: through
	# the remote's name and through the url that remote resolves to. The ref
	# carries the tick, so one origin tells the two source grades apart — the
	# write-grade ticks land theirs, and the read-only review is launched
	# unable to land any. The outcome is not written into the worktree, so no
	# tick's branch or boundary diff changes shape because of it.
	commit
	git -C "$TICFAC_WORKTREE" push origin "HEAD:refs/heads/pushed-by-$TICFAC_TICK" >/dev/null 2>&1
	url=$(git -C "$TICFAC_WORKTREE" remote get-url origin 2>/dev/null || printf '')
	if [ -n "$url" ]; then
		git -C "$TICFAC_WORKTREE" push "$url" "HEAD:refs/heads/pushed-by-url-$TICFAC_TICK" >/dev/null 2>&1
	fi
	report
	;;
touch-undeclared)
	# The tick 01u shape: the worker does its DECLARED work — the same file
	# every mode commits — and then touches a file its declaration does not
	# name. The wave-composition check never sees it (the file was never
	# declared); the declaration's other half does, in the diff the merge
	# reads.
	commit
	printf 'a file nobody declared\n' > "$TICFAC_WORKTREE/sneaky-${TICFAC_TICK}.txt"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: undeclared ${TICFAC_TICK}" >/dev/null 2>&1
	report
	;;
durable-hang)
	# The disk-loss shape (tick lkd): a2's worker — the fixture graph's
	# second wave-1 tick, dispatched beside a1 — does part of its tick's work,
	# makes it DURABLE, and is killed with its whole container before it can
	# finish. The commit is pushed by hand — the supervisor's timer would push
	# it too, but the test that uses this mode waits on the ref, never on a
	# duration — and then the worker hangs forever, because a container that
	# dies mid-tick does not get to say goodbye. Every other tick behaves like
	# the plain report mode, so the killed container is the fixture's one cost.
	#
	# The file is deliberately distinctive, not the usual work-<tick>.txt: a
	# replacement that continues from the pushed branch carries it into the
	# integrated tree, and one that silently redid the work does not — the file
	# is the dead worker's signature, and only continuation can carry it.
	if [ "$TICFAC_TICK" = "a2" ]; then
		printf 'durable work of %s, attempt %s\n' "$TICFAC_TICK" "$TICFAC_ATTEMPT" \
			> "$TICFAC_WORKTREE/durable-${TICFAC_TICK}.txt"
		git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: durable ${TICFAC_TICK}" >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" push -q origin "HEAD:refs/heads/$TICFAC_BRANCH" >/dev/null 2>&1
		exec sleep 86400
	else
		commit
		report
	fi
	;;
hang-boundary)
	# The held attempt a person releases with --carry-work, carrying a record
	# under the tracker's authority (the boundary mode's forged write) among
	# its commits: a carried attempt must not smuggle it past the boundary
	# check into a merge. Then it hangs, as `hang` does.
	mkdir -p "$TICFAC_WORKTREE/.tick/issues"
	printf '{"id":"forged-%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/forged-$TICFAC_TICK.json"
	commit
	exec sleep 86400
	;;
hang-undeclared)
	# The held attempt a person releases with --carry-work, carrying a file
	# its tick's touch: declaration does not name (the touch-undeclared
	# mode's write), in ONE commit so the head a test waits for already
	# holds it. Then it hangs, as `hang` does.
	printf 'a file nobody declared\n' > "$TICFAC_WORKTREE/sneaky-${TICFAC_TICK}.txt"
	commit
	exec sleep 86400
	;;
stuck-first)
	# The epic-6in 823 shape: a1's FIRST TRY commits real work and then goes
	# quiet — the network went away under it — so the stuck watch stops it and
	# it collects as missing-result WITH commits. Every later try (and every
	# other tick) does its work and reports. Keyed on $TICFAC_TRY, never the
	# run-wide attempt number (tick vw0).
	# The nudge re-prompts it in its own session, which runs this script
	# again: it touches nothing the second time, so it stays quiet and the
	# watch stops it.
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_TRY" = "1" ]; then
		[ -e "$TICFAC_WORKTREE/$file" ] || commit
		exec sleep 86400
	fi
	commit
	report
	;;
boundary-first)
	# a1's FIRST TRY writes under the tracker's authority (the boundary mode's
	# forged record) beside its work and reports DONE — rejected on the
	# merits. Every later try is clean.
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_TRY" = "1" ]; then
		mkdir -p "$TICFAC_WORKTREE/.tick/issues"
		printf '{"id":"forged-%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/forged-$TICFAC_TICK.json"
	fi
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
busy-a1)
	# The 9fc shape (tick dh1): a1 keeps writing into its worktree and
	# commits NOTHING until the end, so for most of its life it has no
	# commits, an unmoved branch and — to anything that only measures gaps —
	# a log indistinguishable from the wedged worker beside it. 9fc's own
	# snapshot at the wall clock held 433 uncommitted lines. Every other tick
	# is the plain report mode, so the fixture costs one worker's seconds and
	# not five.
	if [ "$TICFAC_TICK" = "a1" ]; then
		i=0
		while [ "$i" -lt 3 ]; do
			printf 'uncommitted line %s\n' "$i" >> "$TICFAC_WORKTREE/scratch-${TICFAC_TICK}.txt"
			i=$((i + 1))
			sleep 1
		done
	fi
	commit
	report
	;;
wedged)
	# The ef7 shape (tick dh1): an agent that writes NOTHING and never
	# finishes. No commit, no file, no report — the worktree stays exactly as
	# the checkout left it for the whole of the attempt's bound.
	#
	# It is deliberately not `hang` with the commit removed as an
	# afterthought: `hang` commits first, so its branch MOVES and its worktree
	# changes, and it is therefore the WORKING half of the pair this mode
	# exists to be told apart from. On epic ncv the two produced observation
	# logs of the same three lines, and only the count of files written
	# separates them.
	exec sleep 86400
	;;
wedged-a2)
	# The ncv wave, with the roles the run actually saw: a1 does its work and
	# reports, a2 wedges and never writes anything. Both are live at once
	# under a declared width, which is what makes a2 an attempt the run is
	# holding while it spends minutes in another tick's serial half.
	if [ "$TICFAC_TICK" = "a2" ]; then
		exec sleep 86400
	else
		commit
		report
	fi
	;;
evac-live)
	# epic-2jn's shape: a1's worker writes real work, commits nothing, and
	# waits — through the orchestrator's SIGTERM, which a local worker is a
	# separate process from and survives — until $EVAC_GO appears. Then it
	# finishes the way the agent on 2jn did: a commit it finds under it that
	# it never made (a flush's snapshot) is not its own, so it takes the
	# commit back into its index (reset --soft) and commits its result itself,
	# on the head it knew. Every other tick behaves like the report mode.
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_ATTEMPT" = "1" ]; then
		printf 'uncommitted work of %s\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/wip-${TICFAC_TICK}.txt"
		waited=0
		while [ ! -e "${EVAC_GO:-/nonexistent}" ] && [ "$waited" -lt 1200 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		if git -C "$TICFAC_WORKTREE" log -1 --format=%s | grep -q 'evacuation snapshot'; then
			git -C "$TICFAC_WORKTREE" reset -q --soft HEAD~1 >/dev/null 2>&1
		fi
	fi
	commit
	report
	;;
amend-pushed)
	# epic-2jn, rix attempt 45: a1's worker commits, waits until its
	# supervisor's timer has put that commit on origin, and then amends it —
	# a worker rewriting its own already-pushed history, which is ordinary
	# agent behaviour. Every other tick behaves like the report mode.
	commit
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_ATTEMPT" = "1" ]; then
		head="$(git -C "$TICFAC_WORKTREE" rev-parse HEAD)"
		waited=0
		until git -C "$TICFAC_WORKTREE" ls-remote origin "refs/heads/$TICFAC_BRANCH" | grep -q "^$head"; do
			if [ "$waited" -ge 600 ]; then
				echo "fake runner: the supervisor never pushed $head; nothing to amend over" >&2
				exit 1
			fi
			sleep 0.1
			waited=$((waited + 1))
		done
		printf 'amended by %s after it was pushed\n' "$TICFAC_TICK" >> "$TICFAC_WORKTREE/$file"
		git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" commit -q --amend --no-edit >/dev/null 2>&1
	fi
	report
	;;
wallwip)
	# pbb's shape: attempt 1 of a1 does real work in the tree, commits
	# nothing and stays alive past the bound, so the wall clock stops it
	# holding uncommitted work — and the teardown that follows the refusal is
	# where that work used to die unrecorded. Every later attempt of every
	# tick does the work cleanly.
	if [ "$TICFAC_ATTEMPT" = "1" ] && [ "$TICFAC_TICK" = "a1" ]; then
		printf 'uncommitted work of %s\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/wip-${TICFAC_TICK}.txt"
		exec sleep 86400
	else
		commit
		report
	fi
	;;
land_repair)
	# The READY PR (land.go): CI on the epic PR goes red after the base was
	# folded in, and the run's answer is the plan-repair job, whose fake
	# worker commits the fix the failing CI job named (ci-fix.txt, which the
	# test's forge reads as the difference between red and green). A base
	# fold that conflicts is the resolve-conflict job's union, as in the
	# conflict mode. Every other job does its work and reports.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		printf 'the fix the red CI job named\n' > "$TICFAC_WORKTREE/ci-fix.txt"
		git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" commit -q -m "repair: the fix the red CI job named" >/dev/null 2>&1
		report
	elif [ "$TICFAC_ROLE" = "resolve-conflict" ]; then
		resolve_union
		report
	else
		commit
		report
	fi
	;;
finding_live_run)
	# The epic-2jn tm5 shape: the work is done, and the report carries ONE
	# finding whose remedy is a live run of an epic — which no worker can do
	# from inside a tick — claiming done item A1.
	commit
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found something only a live run can prove.\n\n'
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "Demonstrate A1 live: ticfac init + ticfac run in herdr on claude, and one epic via run --cloud",'
		printf '%s\n' '  "body": "A1 was tested with a stand-in; no run started from ticfac init then ticfac run. Run the next epic that way and record the evidence.",'
		printf '%s\n' '  "severity": "medium",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "done_item": "A1",'
		printf '%s\n' '  "demonstrating_check": "none"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
closeout_red)
	# Epic-6in's close-out: CI on the epic's code is red (the test's forge
	# reads red on any tree without ci-fix.txt once the close-out has been
	# dispatched — before that it answers the false green 6in was admitted
	# on). The close-out, cut from a red tree, writes its retro and answers
	# BLOCKED; the plan-repair job commits the fix the failing CI job named;
	# a close-out cut from the repaired tree answers DONE. Every other job
	# does its work and reports.
	if [ "$TICFAC_ROLE" = "plan-repair" ]; then
		printf 'the fix the red CI job named\n' > "$TICFAC_WORKTREE/ci-fix.txt"
		git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
		git -C "$TICFAC_WORKTREE" commit -q -m "repair: the fix the red CI job named" >/dev/null 2>&1
		report
	elif [ "$TICFAC_TICK" = "co" ] && [ ! -e "$TICFAC_WORKTREE/ci-fix.txt" ]; then
		commit
		status="BLOCKED"
		report
	else
		commit
		report
	fi
	;;
closeout_red_uncarried)
	# Epic-6in's v7z: the close-out, cut from the integration branch, writes
	# its retro and answers BLOCKED; a close-out cut from that retro (its work
	# CARRIED) finishes. A fresh close-out would answer BLOCKED again, so the
	# mode tells a carried try from a fresh one.
	if [ "$TICFAC_TICK" = "co" ] && [ ! -e "$TICFAC_WORKTREE/$file" ]; then
		commit
		status="BLOCKED"
		report
	else
		commit
		report
	fi
	;;
stuck-first-then-nothing)
	# stuck-first, and a1's LATER tries find the carried work complete and add
	# nothing: a report over the carried head, no commit of their own. The
	# carried attempt's delivery is the carried work (the epic-6in v7z shape,
	# on an implement tick).
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_TRY" = "1" ]; then
		[ -e "$TICFAC_WORKTREE/$file" ] || commit
		exec sleep 86400
	fi
	[ "$TICFAC_TICK" = "a1" ] || commit
	report
	;;
closeout_red_carried_confirms)
	# Epic-6in's v7z, as it really stalled: the close-out, cut from the
	# integration branch, writes its retro and answers BLOCKED; the close-out
	# cut from that retro (its work CARRIED) finds the retro already done,
	# commits NOTHING and answers DONE_WITH_CONCERNS. Every other job does its
	# work and reports.
	if [ "$TICFAC_TICK" = "co" ] && [ ! -e "$TICFAC_WORKTREE/$file" ]; then
		commit
		status="BLOCKED"
		report
	elif [ "$TICFAC_TICK" = "co" ]; then
		status="DONE_WITH_CONCERNS"
		report
	else
		commit
		report
	fi
	;;
closeout_asks_twice)
	# A close-out that stops to ask twice and finishes on its third try —
	# but only a try that CARRIES the earlier tries' work: each asking try
	# appends a line to asks-co.txt, and a try that finds two lines (its
	# worktree was cut from both earlier tries' commits) finishes. A fresh
	# try finds none and asks again.
	if [ "$TICFAC_TICK" = "co" ]; then
		asks=0
		if [ -e "$TICFAC_WORKTREE/asks-co.txt" ]; then
			asks=$(wc -l < "$TICFAC_WORKTREE/asks-co.txt" | tr -d ' ')
		fi
		if [ "$asks" -lt 2 ]; then
			printf 'asked on attempt %s\n' "$TICFAC_ATTEMPT" >> "$TICFAC_WORKTREE/asks-co.txt"
			commit
			status="BLOCKED"
			report
		else
			commit
			report
		fi
	else
		commit
		report
	fi
	;;
finding_build_red)
	# The prose rule's exception (epic-6in): the work is done, and the report
	# carries ONE in-repository finding whose reporter claims it breaks the
	# build — which gates any done, prose or not.
	commit
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found the build broken.\n\n'
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "A re-run holds forever on a stale claim (regression; full suite red)",'
		printf '%s\n' '  "body": "The full suite fails on the regression test.",'
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "done_item": "none",'
		printf '%s\n' '  "demonstrating_check": "go"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
		printf '\n'
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
*)
	printf 'unknown FAKE_RUNNER_MODE %s\n' "$mode" >&2
	exit 64
	;;
esac
