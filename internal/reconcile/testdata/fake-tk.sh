#!/bin/sh
# tk's two merge drivers, with tk taken out.
#
# Git invokes a custom merge driver THROUGH THE SHELL, with four arguments —
# %O %A %B %P — and reads the result from %A, not from stdout. This stands in
# for `tk merge-file` and `tk merge-activity` so that a fold of tracker records
# is observable without a tracker binary on the machine, and it is deliberately
# strict about the argument count: a driver invoked with the wrong number of
# arguments is the mistake the manifest's argv exists to prevent.
#
# Every invocation is appended to $FAKE_TK_LOG, so a test can say what git
# actually ran rather than inferring it from a file that merged.
set -u

log="${FAKE_TK_LOG:-/dev/null}"
printf '%s\n' "$*" >>"$log"

command="$1"
shift

case "$command" in
merge-file | merge-activity)
	if [ "$#" -ne 4 ]; then
		printf 'fake-tk: %s takes four arguments, got %s\n' "$command" "$#" >&2
		exit 1
	fi
	ours="$2"
	theirs="$3"
	# The union tk resolves these formats to: every line either side has, in
	# order, once. Written back over OURS, which is where git reads a merge
	# driver's result from.
	if ! awk '!seen[$0]++' "$ours" "$theirs" >"$ours.merged"; then
		exit 1
	fi
	mv "$ours.merged" "$ours"
	;;
*)
	printf 'fake-tk: no command %s\n' "$command" >&2
	exit 1
	;;
esac
