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

report() {
	mkdir -p "$(dirname "$TICFAC_RESULT_PATH")"
	{
		printf '# %s\n\n' "${TICFAC_TICK}"
		printf 'The fake runner ran in mode %s.\n\n' "$mode"
		printf 'STATUS: %s\n' "$status"
	} > "$TICFAC_RESULT_PATH"
}

case "$mode" in
report)
	commit
	report
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
silent)
	# Settled and incomplete: work committed, nothing said.
	commit
	;;
nocommit)
	report
	;;
boundary)
	mkdir -p "$TICFAC_WORKTREE/.tick/issues"
	printf '{"id":"%s","status":"closed"}\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/.tick/issues/$TICFAC_TICK.json"
	printf 'a learning\n' >> "$TICFAC_WORKTREE/.tick/learnings.md"
	commit
	report
	;;
hang)
	# Commits, then never finishes: the shape of a worker that is killed.
	# `exec` replaces this shell with the sleeper, so the runner's process
	# group has exactly one member for a group-kill to reach, never a
	# transient window with a forked child the group signal could miss.
	commit
	exec sleep 86400
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
