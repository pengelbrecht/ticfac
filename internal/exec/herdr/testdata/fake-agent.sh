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
#   - interrupted (agent.send_keys ctrl+c), it stops spending and exits.
#
# argv: <worktree> <prompt-file> <status-file> <interrupt-file> <mode>
#
# modes:
#   implement  read the tick record, do what it says, report DONE
#   sleep      report working, then wait to be interrupted
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

report_done
