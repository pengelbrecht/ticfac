#!/bin/sh
# A fake runner: what claude, codex or pi would be, with the agent taken out.
#
# The unit tests drive the executor through this rather than through a real
# agent for the reason SPEC §12 Phase 1 step 3 gives — the three runners are
# ONE executor, and what has to be tested is the executor: the worktree, the
# report path it owns, the timer, the boundary and the settlement rules. An
# agent in the loop would make every one of those tests non-deterministic
# without making any of them stronger. The opt-in live test is what says the
# argv table is real.
#
# It behaves as FAKE_RUNNER_MODE says. The prompt arrives as $1 and is ignored
# except by the `echo_prompt` mode, which proves it was handed over at all.
set -u

prompt="${1:-}"
mode="${FAKE_RUNNER_MODE:-report}"
status="${FAKE_RUNNER_STATUS:-DONE}"

commit() {
	printf 'work by the fake runner: %s\n' "$mode" > "$TICFAC_WORKTREE/worked.txt"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: ${mode}" >/dev/null 2>&1
}

verdict_line() {
	# The review's typed verdict line (tick b50): only the review-epic role is
	# asked for one, so only that role writes one — a stray line in another
	# role's report is ignored by the parser and never reaches a payload.
	if [ "$TICFAC_ROLE" = "review-epic" ]; then
		printf 'REVIEW-VERDICT: %s\n' "${FAKE_RUNNER_REVIEW_VERDICT:-READY}"
	fi
}

report() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "${TICFAC_TICK}"
		printf 'The fake runner ran in mode %s.\n\n' "$mode"
		verdict_line
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

case "$mode" in
report)
	commit
	report
	;;
busy_then_hang|hang)
	# The stuck watch (tick wv2). Re-prompted as stuck (TICFAC_STUCK_NUDGE),
	# busy_then_hang finishes: a commit and the report. Otherwise it is a
	# tool call that hangs: busy_then_hang first burns CPU in a child process
	# for FAKE_RUNNER_BUSY seconds — a test suite that is working, not stuck —
	# then both modes sit in a child that uses no CPU and writes nothing.
	#
	# `hang` is also every killed-worker test's runner, and it forks — git,
	# then the sleeper — the way a real worker does. It used to have a case of
	# its own below that exec'd the sleeper so a group kill had one member to
	# reach; this case shadowed it (the first matching pattern wins), and the
	# forked child a single group kill can miss is exactly what the stop paths
	# must handle anyway (killUntilGone), so there is one hang, and it forks.
	if [ -n "${TICFAC_STUCK_NUDGE:-}" ] && [ "$mode" = "busy_then_hang" ]; then
		commit
		report
		exit 0
	fi
	if [ "$mode" = "busy_then_hang" ]; then
		sh -c 'while :; do :; done' &
		burner=$!
		sleep "${FAKE_RUNNER_BUSY:-3}"
		kill "$burner" 2>/dev/null
		wait "$burner" 2>/dev/null
	fi
	sleep 3600
	;;
review_not_ready)
	# The b50 shape at the executor: the review's report carries its typed
	# verdict — DONE_WITH_CONCERNS in the status vocabulary, NOT READY in its
	# own — over an empty branch, which is what a correct review attempt looks
	# like. Only a review-epic job has a verdict to state, so every other job
	# is exactly the plain report mode.
	if [ "$TICFAC_ROLE" = "review-epic" ]; then
		status="DONE_WITH_CONCERNS"
		FAKE_RUNNER_REVIEW_VERDICT="NOT READY — the reconciler was never wired to the run"
		# A NOT READY names its blocking finding (epic-6in): a high one.
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "${TICFAC_TICK}"
			printf '%s\n' '```findings v2'
			printf '%s\n' '[{"kind": "defect", "title": "The reconciler is never wired to the run", "severity": "high"}]'
			printf '%s\n\n' '```'
			verdict_line
			printf 'STATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
	else
		commit
		report
	fi
	;;
report_from_tmp)
	# The case the absolute path exists for: the worker wanders off and then
	# writes its report from somewhere else entirely.
	commit
	cd /tmp || exit 1
	report
	;;
echo_prompt)
	commit
	printf '%s' "$prompt" > "$TICFAC_WORKTREE/prompt-seen.txt"
	report
	;;
maintenance_seen)
	# What the runner's own git is told about automatic maintenance (tick
	# mel): read through `git config`, so it is the answer git itself acts on.
	git -C "$TICFAC_WORKTREE" config --get maintenance.auto > "$TICFAC_WORKTREE/maintenance-seen.txt" 2>/dev/null
	commit
	report
	;;
silent)
	# Settled and incomplete: work committed, nothing said, exit 0 — also
	# when re-prompted (nudge.go), where the commit finds nothing new.
	commit
	exit 0
	;;
nocommit)
	report
	;;
stop_early)
	# epic-2jn vqc (2026-09-27): claude started the gate in the background,
	# ended its turn to "wait for the notification", and print mode exited 0
	# with one commit and no report. Re-prompted (TICFAC_NUDGE set), it
	# finishes: a second commit carrying the prompt it was nudged with, then
	# the report.
	if [ -z "${TICFAC_NUDGE:-}" ]; then
		commit
		exit 0
	fi
	printf '%s' "$prompt" > "$TICFAC_WORKTREE/nudge-${TICFAC_NUDGE}.txt"
	git -C "$TICFAC_WORKTREE" add -A >/dev/null 2>&1
	git -C "$TICFAC_WORKTREE" commit -q -m "fake runner: finished after nudge ${TICFAC_NUDGE}" >/dev/null 2>&1
	report
	;;
background_env)
	# What the runner's own environment says about background tasks: the
	# setting runner.go gives claude.
	commit
	printf '%s' "${CLAUDE_CODE_DISABLE_BACKGROUND_TASKS:-unset}" > "$TICFAC_WORKTREE/background-env.txt"
	report
	;;
findings)
	# The findings channel (tick 7vn): a worker that commits, reports DONE,
	# and reports two discoveries outside its tick as a typed block — one for
	# this repository and one routed upstream. Since tick nfo the first also
	# carries its DONE EVIDENCE and the second deliberately carries none: a
	# linked finding and an unlinked one are both shapes the real worker
	# produces, and collect must lift both honestly.
	commit
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner also found things outside its tick.\n\n'
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "proposed-tick",'
		printf '%s\n' '  "title": "A finding the fake runner proposes",'
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "medium",'
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
		printf '\nSTATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
findings_bad)
	# A findings block that does not parse: collect must carry the problem
	# rather than dropping the block — dropping findings is what the channel
	# exists to prevent.
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
findings_fixed_on_pushback)
	# The report check's pushback (tick 4m6): the first turn commits and writes
	# a report whose findings block does not parse. Pushed back
	# (TICFAC_LINT_PUSHBACK set), it keeps the prompt it was pushed back with
	# beside the attempt record and rewrites the report with a v2 block that
	# reads.
	if [ -z "${TICFAC_LINT_PUSHBACK:-}" ]; then
		commit
		mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
		{
			printf '# %s\n\n' "$TICFAC_TICK"
			printf '%s\n' '```findings v2'
			printf '%s\n' '[{"kind": "defect", "title": "a block that never ends",'
			printf '%s\n' '```'
			printf '\nSTATUS: %s\n' "$status"
		} > "$TICFAC_RESULT_PATH"
		exit 0
	fi
	printf '%s' "$prompt" > "$TICFAC_WORKTREE/../pushback-${TICFAC_LINT_PUSHBACK}.txt"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf '%s\n' '```findings v2'
		printf '%s\n' '[{"kind": "defect", "title": "a block that reads now", "severity": "low"}]'
		printf '%s\n' '```'
		printf '\nSTATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
review_verdict_on_pushback)
	# A review that states its judgement only in prose (tick b50's refusal
	# case), pushed back by the report check (tick 4m6): the second turn adds
	# the typed REVIEW-VERDICT line.
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The epic is ready, the review says in prose.\n\n'
		if [ -n "${TICFAC_LINT_PUSHBACK:-}" ]; then
			printf '%s' "$prompt" > "$TICFAC_WORKTREE/../pushback-${TICFAC_LINT_PUSHBACK}.txt"
			printf 'REVIEW-VERDICT: READY\n'
		fi
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
findings_folds)
	# The fold case (tick ryv): a finding carrying a key the record does not
	# know — the 3h0 shape, an extra "title_note" — from a worker whose work
	# is otherwise DONE. Collect must ACCEPT the finding with the key folded
	# into its body as a labelled line, carry the fold so the attempt's
	# records can note it, and not turn the annotation into a refusal.
	commit
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "$TICFAC_TICK"
		printf 'The fake runner reported a finding with an extra key.\n\n'
		printf '%s\n' '```findings'
		printf '%s\n' '[{'
		printf '%s\n' '  "kind": "defect",'
		printf '%s\n' '  "title": "A finding carrying an extra key",'
		printf '%s\n' '  "body": "Discovered beside the work, reported mechanically.",'
		printf '%s\n' '  "severity": "medium",'
		printf '%s\n' '  "target": "",'
		printf '%s\n' '  "title_note": "an annotation the record has no field for"'
		printf '%s\n' '}]'
		printf '%s\n' '```'
		printf '\nSTATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
	;;
report_then_addall)
	# The shape wtd exists for: the report is written FIRST, then the worker
	# runs `git add -A` and commits everything in the worktree, exactly the
	# way the uqe worker's report ended up on the gate-cia-2 attempt branch.
	# The executor's own git exclude (written at Start, before this runner
	# ever ran) must keep the report off the branch regardless.
	report
	commit
	;;
force_report)
	# The negative case the collect backstop exists for: a worker that
	# force-adds the excluded report (`git add -f`) bypasses the exclude
	# outright. collect must still catch it off the branch diff.
	report
	git -C "$TICFAC_WORKTREE" add -f "$TICFAC_RESULT_PATH" >/dev/null 2>&1
	commit
	;;
push)
	# The runner tries to advance a ref itself, which is the thing a source
	# grade has to make impossible rather than merely decline to do for it.
	# Two pushes: through the remote NAME and through the remote's URL, since
	# an explicit URL bypasses remote.<name>.pushurl. The outcome goes in the
	# STATE directory (the worktree's parent), never in the worktree, so the
	# attempt's own branch and boundary diff stay exactly what they would be.
	commit
	out="$TICFAC_WORKTREE/../push"
	origin_url=$(git -C "$TICFAC_WORKTREE" remote get-url origin 2>/dev/null || printf '')
	{
		printf '## by name\n'
		git -C "$TICFAC_WORKTREE" push origin HEAD:refs/heads/pushed-by-runner 2>&1
		printf 'exit %s\n' "$?"
		printf '## by url (%s)\n' "$origin_url"
		if [ -n "$origin_url" ]; then
			git -C "$TICFAC_WORKTREE" push "$origin_url" HEAD:refs/heads/pushed-by-url 2>&1
			printf 'exit %s\n' "$?"
		fi
	} > "$out.log" 2>&1
	report
	;;
boundary)
	mkdir -p "$TICFAC_WORKTREE/.tick/issues"
	printf '{"id":"%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/$TICFAC_TICK.json"
	printf 'a learning\n' >> "$TICFAC_WORKTREE/.tick/learnings.md"
	commit
	report
	;;
report_then_hang)
	# The shape a cancel has to get right: the report is written and the worker
	# is STILL RUNNING. Real workers do this constantly — they write the report
	# and then commit, tidy up, or run one more check — and a cancel that read
	# the report and called the attempt settled would revoke, kill the process
	# tree, record NO refusal, and acknowledge that no stop was requested.
	commit
	report
	exec sleep 86400
	;;
report_then_linger)
	# The tail of a finished worker (hol): the report is written, and the
	# runner exits a moment later, on its own. A cancel that lands in that
	# moment is a release of finished work, not a stop of a spending one.
	commit
	report
	sleep "${FAKE_RUNNER_LINGER:-2}"
	;;
quota_exhausted)
	# No commit, no report: the shape of a codex run that hit its flat-rate
	# seat's usage limit before writing anything. The log is golden —
	# testdata/codex-usage-limit.log, a real capture from the qg5 live test
	# runner log (stderr, exit 1, 2026-09-02 19:56).
	cat "$(dirname "$0")/codex-usage-limit.log" >&2
	exit 1
	;;
pi_out_of_usage)
	# No commit, no report: the shape of a pi run whose API account is out of
	# extra usage before writing anything. pi's error does not share codex's
	# wording at all, which is why the classifier's pattern is an explicit
	# alternation rather than one guessed to cover both. The log is golden —
	# testdata/pi-out-of-usage.log, a real capture from the pi live test
	# (stdout/stderr, exit 1, 2026-09-02 22:12).
	cat "$(dirname "$0")/pi-out-of-usage.log" >&2
	exit 1
	;;
usage_error)
	# No commit, no report, exit 2: the shape of an argv this executor got
	# wrong, so the runner refuses before it ever reaches the model.
	printf 'codex: unknown flag: --bogus-flag\n' >&2
	printf 'Usage: codex exec [flags] <prompt>\n' >&2
	exit 2
	;;
slow_report)
	commit
	sleep "${FAKE_RUNNER_SLEEP:-2}"
	report
	;;
*)
	printf 'unknown FAKE_RUNNER_MODE %s\n' "$mode" >&2
	exit 64
	;;
esac
