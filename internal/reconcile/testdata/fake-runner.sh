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
}

in_conflict_ticks() {
	case " ${CONFLICT_TICKS:-} " in *" $TICFAC_TICK "*) return 0 ;; esac
	return 1
}
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
finding)
	# The discovery case: the work is done, the report is DONE, and the
	# report also carries a typed findings block — one finding for this
	# repository and one routed upstream.
	commit
	report_with_findings
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
	# ready-to-merge, and carry it to the epic PR. Every other tick is the
	# plain report mode.
	if [ "$TICFAC_TICK" = "rv" ]; then
		status="DONE_WITH_CONCERNS"
		FAKE_RUNNER_REVIEW_VERDICT="NOT READY — the Phase 4 gate run never happened"
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
*)
	printf 'unknown FAKE_RUNNER_MODE %s\n' "$mode" >&2
	exit 64
	;;
esac
