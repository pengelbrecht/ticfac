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

findings_block() {
	# The findings channel (tick 7vn): the typed block a worker reports its
	# discoveries in. Two findings, deliberately different in target: one for
	# the repository being run, one routed upstream — a worker that discovers
	# something on ANOTHER tracker names which one, and the draft keeps it.
	{
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "A finding the fake runner proposes",'
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "high",'
		printf '%s\n' '  "target": ""'
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
		printf '\nSTATUS: %s\n' "$status"
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
	# The v3i shape: the FIRST attempt of a1 finds a blocker of its own and
	# answers BLOCKED with nothing committed — but its report also carries a
	# typed finding, so the discovery is drafted even though the attempt is
	# refused. Every later attempt of a1 commits, answers DONE, and reports
	# the SAME finding: the dedup case, a finding that recurs until it is
	# fixed. Other ticks behave like the plain report mode.
	if [ "$TICFAC_TICK" = "a1" ] && [ "$TICFAC_ATTEMPT" = "1" ]; then
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
