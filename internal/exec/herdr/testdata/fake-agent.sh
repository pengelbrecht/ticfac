#!/bin/sh
# The fake agent: the one component these tests replace, exactly as the local
# executor's harness replaces the runner.
#
# The contract it stands in for is a real herdr agent's:
#   - it starts at a prompt and waits for work;
#   - it reads the work from the prompt delivered to it;
#   - it reads the tick record in its worktree and does the work;
#   - it commits the work on its branch, writes its report at the absolute
#     path the prompt owns, and reports done;
#   - interrupted (agent.send_keys), it stops spending and exits.
#
# argv: <worktree> <prompt-file> <status-file> <interrupt-file> <mode>
#
# modes:
#   implement           read the tick record, do what it says, report DONE
#   sleep               report working, then wait to be interrupted
#   esc_only            pi's keybindings (epic-6in, 2026-09-28): report
#                       working; Escape (app.interrupt) aborts the turn and
#                       the agent goes back to its prompt — `idle`, still
#                       running, never exiting and never reporting; ctrl+c
#                       (app.clear) clears the editor and stops nothing
#   ignore              report working and NEVER stop: the interrupt file
#                       lands and is never honoured — the agent inside a
#                       long shell call that never reads it (tick rj0), the
#                       shape only closing the pane stops
#   report_then_addall  write the report, then `git add -A` and commit — an
#                       ordinary add-all must not stage the excluded report
#   force_report        force-add the excluded report past the exclude
#                       (`git add -f`), the shape collect's backstop exists for
#   force_report_slow   the same force-add, but with a two-second pause
#                       between the report write and the commit — the window
#                       tick wmw made deterministic: an inspect answered from
#                       the report (the completion contract) is TERMINAL while
#                       the agent still has a commit to land, so a test that
#                       synchronizes on Inspect().Terminal collects inside the
#                       window and sees a branch the forced report never
#                       reached
set -e

worktree=$1
prompt_file=$2
status_file=$3
interrupt_file=$4
mode=${5:-implement}

report_working() { printf 'working\n' > "$status_file"; }
report_done() { printf 'done\n' > "$status_file"; }

wait_for_prompt() {
	i=0
	while [ ! -f "$prompt_file" ]; do
		i=$((i + 1))
		if [ "$i" -gt 600 ]; then
			echo "fake agent: no prompt within 30s" >&2
			exit 1
		fi
		sleep 0.05
	done
}

# The report path is the one line the executor indents four spaces after the
# "EXACT ABSOLUTE PATH" heading — the same contract a real agent reads.
report_path() {
	sed -n 's/^    \(.*RESULT-.*\.md\)$/\1/p' "$prompt_file" | head -1
}

tick_id() {
	sed -n 's/^- tick: \(.*\)$/\1/p' "$prompt_file" | head -1
}

if [ "$mode" = "sleep" ]; then
	wait_for_prompt
	report_working
	while [ ! -f "$interrupt_file" ]; do
		sleep 0.1
	done
	echo "fake agent: interrupted; stopping without committing" >&2
	exit 130
fi

if [ "$mode" = "esc_only" ]; then
	# Only Escape interrupts a turn. The interrupt file holds the key
	# sequence of the LAST agent.send_keys, space-separated.
	wait_for_prompt
	report_working
	while :; do
		if [ -f "$interrupt_file" ] && grep -qw esc "$interrupt_file"; then
			printf 'idle\n' > "$status_file"
		fi
		sleep 0.05
	done
fi

if [ "$mode" = "ignore" ]; then
	# The interrupt lands and is never read: the Phase 3 shape (tick emk),
	# where the wall clock's ctrl+c is a courtesy the agent never sees
	# because it is inside a shell call. Nothing but closing the pane
	# stops this agent.
	wait_for_prompt
	report_working
	while :; do
		sleep 0.1
	done
fi

if [ "$mode" = "bad_findings_then_fixed" ] || [ "$mode" = "bad_findings_forever" ]; then
	# The report check's pushback (tick 4m6): the worker commits and writes a
	# report whose findings block does not parse, then ends its turn. Pushed
	# back (a new prompt saying the report fails the check), the _then_fixed
	# worker rewrites it with a block that reads; the _forever worker ends
	# every turn the same way. Each pushback is counted in
	# <prompt-file>.pushbacks.
	wait_for_prompt
	report_working
	printf 'the work\n' > "$worktree/work.txt"
	git -C "$worktree" add work.txt
	git -C "$worktree" commit --quiet -m "the work"
	out=$(report_path)
	mkdir -p "$(dirname "$out")"
	printf '## Report\n\n```findings v2\n[{"kind": "defect",\n```\n\nSTATUS: DONE\n' > "$out"
	rm -f "$prompt_file"
	report_done
	pushbacks=0
	while :; do
		if grep -q "does not pass the report check" "$prompt_file" 2>/dev/null; then
			pushbacks=$((pushbacks + 1))
			printf '%s\n' "$pushbacks" > "$prompt_file.pushbacks"
			rm -f "$prompt_file"
			report_working
			if [ "$mode" = "bad_findings_then_fixed" ]; then
				printf '## Report\n\n```findings v2\n[{"kind": "defect", "title": "fixed", "severity": "low"}]\n```\n\nSTATUS: DONE\n' > "$out"
			fi
			report_done
		fi
		sleep 0.05
	done
fi

if [ "$mode" = "stop_early" ] || [ "$mode" = "never_report" ]; then
	# epic-2jn vqc (2026-09-27): the worker commits, starts the gate "in the
	# background" and ends its turn to wait for the notification — idle, no
	# report. stop_early finishes when re-prompted (the nudge: a new prompt
	# saying the report is missing); never_report ends every turn the same
	# way. Each nudge it received is counted in <prompt-file>.nudges.
	wait_for_prompt
	report_working
	printf 'partial work\n' > "$worktree/partial.txt"
	git -C "$worktree" add partial.txt
	git -C "$worktree" commit --quiet -m "partial work, then the turn ended"
	out=$(report_path)
	report_done
	nudges=0
	while :; do
		if grep -q "ended your turn without writing your report" "$prompt_file" 2>/dev/null; then
			nudges=$((nudges + 1))
			printf '%s\n' "$nudges" > "$prompt_file.nudges"
			rm -f "$prompt_file"
			report_working
			if [ "$mode" = "stop_early" ]; then
				mkdir -p "$(dirname "$out")"
				printf '## Report\n\nFinished after nudge %s.\n\nSTATUS: DONE\n' "$nudges" > "$out"
				report_done
				exit 0
			fi
			report_done
		fi
		sleep 0.05
	done
fi

wait_for_prompt
report_working

# Read the unit of work the way the prompt says to: the tick record in this
# worktree.
id=$(tick_id)
record="$worktree/.tick/issues/$id.json"
if [ ! -f "$record" ]; then
	echo "fake agent: no tick record at $record" >&2
	exit 1
fi

# The task: create the file the record says, with the word it says, and
# commit it. Extracted from the record's description so the work really is
# driven by the tick the prompt named.
name=$(sed -n 's/.*Create the file \([a-z.]*\) containing.*/\1/p' "$record" | head -1)
word=$(sed -n 's/.*containing exactly the word \([a-z]*\),.*/\1/p' "$record" | head -1)
if [ -z "$name" ] || [ -z "$word" ]; then
	echo "fake agent: cannot read the task from $record" >&2
	exit 1
fi

printf '%s\n' "$word" > "$worktree/$name"
git -C "$worktree" add "$name"
git -C "$worktree" commit --quiet -m "tick $id: $word"

# The report, at the absolute path the executor owns, ending in the status
# line the collect vocabulary reads.
out=$(report_path)
if [ -z "$out" ]; then
	echo "fake agent: the prompt named no report path" >&2
	exit 1
fi
mkdir -p "$(dirname "$out")"
{
	echo "## Report"
	echo ""
	echo "Created $name containing the word $word and committed it on the attempt branch."
	echo ""
	echo "STATUS: DONE"
} > "$out"

if [ "$mode" = "report_then_addall" ]; then
	# The p6b prevention fixture: the report is written FIRST, then the
	# worker runs an ordinary `git add -A` over the worktree — the way two of
	# four wave-2 workers put their RESULT file on the attempt branch. The
	# exclude Start wrote before this agent ever ran must keep it off.
	printf 'staged by the ordinary add-all\n' > "$worktree/addall.txt"
	git -C "$worktree" add -A
	git -C "$worktree" commit --quiet -m "tick $id: add all"
fi
if [ "$mode" = "force_report" ] || [ "$mode" = "force_report_slow" ]; then
	# The p6b detection fixture: a worker that bypasses the exclude outright
	# (`git add -f` on the excluded path). collect must still catch it off
	# the branch diff and report it as a boundary violation. The _slow
	# variant holds the window between the report write above and this
	# commit open for two seconds, which is what makes the wmw race
	# reproducible on demand instead of only under a loaded suite.
	if [ "$mode" = "force_report_slow" ]; then
		sleep 2
	fi
	git -C "$worktree" add -f "$out"
	git -C "$worktree" commit --quiet -m "tick $id: the report rides the branch"
fi

report_done
