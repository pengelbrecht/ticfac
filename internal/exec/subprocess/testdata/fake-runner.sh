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
	commit
	while :; do sleep 1; done
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
