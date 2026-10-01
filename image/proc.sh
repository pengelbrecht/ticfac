#!/usr/bin/env bash
#
# ticks-proc — background processes in a container nobody else supervises.
#
# The Sandbox SDK 0.x image runs a control server that owns processes for the
# Worker (startProcess / getProcess / listProcesses / streamProcessLogs /
# killProcess). A container started through a Durable Object's own
# `ctx.container` under the durable_object scheduling policy (epic umq) has
# no such server: the Worker can only `exec` a command and see it finish. This
# is the documented replacement (Sandbox SDK migrate guide, "background
# processes"): one directory per process, and a short command per question.
# Every call returns at once, so each one is a plain exec.
#
# It coexists with the 0.x control server, which neither knows nor uses it.
#
# Layout, under $TICKS_PROC_ROOT (default /var/run/ticks-proc), per id:
#   argv          the command, one argument per line (for a person reading it)
#   cwd           the directory it was started in
#   supervisor    pid of the waiting shell that records the exit code
#   pid           pid of the process itself, which leads its own process group
#   stdout        its standard output, appended as it is written
#   stderr        its standard error, likewise
#   exit          its exit status (128+N for a death by signal N), written
#                 atomically once it has exited and only then
#
# Usage:
#   ticks-proc start <id> [--cwd <dir>] -- <command> [args...]
#   ticks-proc status <id>        state=starting|running|exited|lost, then
#                                 pid=<pid> and, once exited, exit_code=<n>
#   ticks-proc read <id> <stdout|stderr> [offset]
#                                 the stream's bytes from byte <offset> (default 0)
#   ticks-proc kill <id> [--signal <SIG>] [--grace <seconds>]
#                                 signals the process group (TERM by default),
#                                 then KILLs it if it outlives the grace (10 s);
#                                 prints the status it ended in
#   ticks-proc list               every id, one per line
#
# `lost` means neither the process nor its supervisor is alive and no exit
# status was recorded: the container restarted, or something KILLed the
# supervisor. Nothing will ever write `exit` for it.
#
# Exit: 0 on success; 2 for a usage error; 3 for an unknown id; 4 when start
# finds the id already taken.
set -uo pipefail

readonly ME="ticks-proc"
readonly ROOT="${TICKS_PROC_ROOT:-/var/run/ticks-proc}"

usage() {
	printf 'usage: %s start <id> [--cwd <dir>] -- <command> [args...]\n' "$ME" >&2
	printf '       %s status <id>\n' "$ME" >&2
	printf '       %s read <id> <stdout|stderr> [offset]\n' "$ME" >&2
	printf '       %s kill <id> [--signal <SIG>] [--grace <seconds>]\n' "$ME" >&2
	printf '       %s list\n' "$ME" >&2
	exit 2
}

die() {
	printf '%s: %s\n' "$ME" "$1" >&2
	exit "${2:-1}"
}

# An id is a directory name: no path separators, no dot-names.
valid_id() {
	[[ $1 =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]]
}

proc_dir() {
	valid_id "$1" || die "invalid process id: $1" 2
	printf '%s/%s' "$ROOT" "$1"
}

known_dir() {
	local dir
	dir="$(proc_dir "$1")" || exit $?
	[[ -d $dir ]] || die "no such process: $1" 3
	printf '%s' "$dir"
}

# A zombie answers kill -0, and in a container whose PID 1 does not reap
# orphans a dead supervisor stays one forever, so a zombie counts as dead.
alive() {
	[[ -n $1 ]] && kill -0 "$1" 2>/dev/null || return 1
	local stat
	if [[ -r /proc/$1/stat ]] && stat="$(</proc/"$1"/stat)" 2>/dev/null; then
		stat=${stat##*) }
		[[ ${stat:0:1} != Z ]]
		return
	fi
	stat="$(ps -o stat= -p "$1" 2>/dev/null)" || return 0
	[[ $stat != Z* ]]
}

# The state of the process in $1 (its directory), on stdout as key=value lines.
state_of() {
	local dir=$1 pid="" sup=""
	[[ -f $dir/pid ]] && pid="$(<"$dir/pid")"
	[[ -f $dir/supervisor ]] && sup="$(<"$dir/supervisor")"
	if [[ -f $dir/exit ]]; then
		printf 'state=exited\npid=%s\nexit_code=%s\n' "$pid" "$(<"$dir/exit")"
		return
	fi
	if alive "$sup"; then
		if [[ -z $pid ]]; then
			printf 'state=starting\npid=\n'
		else
			printf 'state=running\npid=%s\n' "$pid"
		fi
		return
	fi
	# The supervisor may have written `exit` between the two reads above.
	if [[ -f $dir/exit ]]; then
		printf 'state=exited\npid=%s\nexit_code=%s\n' "$pid" "$(<"$dir/exit")"
		return
	fi
	printf 'state=lost\npid=%s\n' "$pid"
}

# The supervisor: runs as a detached shell, starts the command in its own
# process group (job control), records its pid, waits, records its status.
supervise() {
	local dir=$1
	shift
	set -m
	"$@" >>"$dir/stdout" 2>>"$dir/stderr" </dev/null &
	local child=$!
	printf '%s\n' "$child" >"$dir/pid.tmp" && mv "$dir/pid.tmp" "$dir/pid"
	local status
	while :; do
		wait "$child"
		status=$?
		# wait returns early (>128) when the supervisor itself is signalled;
		# only a child that is gone has really exited.
		alive "$child" || break
	done
	printf '%s\n' "$status" >"$dir/exit.tmp" && mv "$dir/exit.tmp" "$dir/exit"
}

cmd_start() {
	[[ $# -ge 1 ]] || usage
	local id=$1 cwd=$PWD
	shift
	while [[ $# -gt 0 ]]; do
		case $1 in
		--cwd)
			[[ $# -ge 2 ]] || usage
			cwd=$2
			shift 2
			;;
		--)
			shift
			break
			;;
		*) usage ;;
		esac
	done
	[[ $# -ge 1 ]] || usage
	[[ -d $cwd ]] || die "no such directory: $cwd" 2
	local dir
	dir="$(proc_dir "$id")" || exit $?
	mkdir -p "$ROOT" || die "cannot create $ROOT"
	# mkdir is the claim: two starts of one id cannot both succeed.
	mkdir "$dir" 2>/dev/null || die "process id already taken: $id" 4
	printf '%s\n' "$@" >"$dir/argv"
	printf '%s\n' "$cwd" >"$dir/cwd"
	: >"$dir/stdout"
	: >"$dir/stderr"
	(
		cd "$cwd" || exit 1
		trap '' HUP
		supervise "$dir" "$@" &
		printf '%s\n' "$!" >"$dir/supervisor.tmp" && mv "$dir/supervisor.tmp" "$dir/supervisor"
	) </dev/null >/dev/null 2>&1
	# Return once the process exists, so a status right after a start never
	# answers about a process that has not been forked yet.
	local i
	for ((i = 0; i < 100; i++)); do
		[[ -f $dir/pid ]] && break
		sleep 0.05
	done
	printf '%s\n' "$id"
}

cmd_status() {
	[[ $# -eq 1 ]] || usage
	local dir
	dir="$(known_dir "$1")" || exit $?
	state_of "$dir"
}

cmd_read() {
	[[ $# -ge 2 && $# -le 3 ]] || usage
	local stream=$2 offset=${3:-0}
	[[ $stream == stdout || $stream == stderr ]] || usage
	[[ $offset =~ ^[0-9]+$ ]] || die "offset must be a byte count: $offset" 2
	local dir
	dir="$(known_dir "$1")" || exit $?
	tail -c "+$((offset + 1))" "$dir/$stream"
}

cmd_kill() {
	[[ $# -ge 1 ]] || usage
	local id=$1 signal=TERM grace=10
	shift
	while [[ $# -gt 0 ]]; do
		case $1 in
		--signal)
			[[ $# -ge 2 ]] || usage
			signal=${2#SIG}
			shift 2
			;;
		--grace)
			[[ $# -ge 2 && $2 =~ ^[0-9]+$ ]] || usage
			grace=$2
			shift 2
			;;
		*) usage ;;
		esac
	done
	local dir
	dir="$(known_dir "$id")" || exit $?
	local i
	for ((i = 0; i < 100; i++)); do
		[[ -f $dir/pid || -f $dir/exit ]] && break
		sleep 0.05
	done
	local pid=""
	[[ -f $dir/pid ]] && pid="$(<"$dir/pid")"
	if [[ ! -f $dir/exit ]] && alive "$pid"; then
		# The process leads its own group: signal it and everything it started.
		kill -s "$signal" -- "-$pid" 2>/dev/null || kill -s "$signal" "$pid" 2>/dev/null
		for ((i = 0; i < grace * 10; i++)); do
			[[ -f $dir/exit ]] && break
			sleep 0.1
		done
		if [[ ! -f $dir/exit ]] && alive "$pid"; then
			kill -s KILL -- "-$pid" 2>/dev/null || kill -s KILL "$pid" 2>/dev/null
		fi
		# Give the supervisor a moment to record the status.
		for ((i = 0; i < 50; i++)); do
			[[ -f $dir/exit ]] && break
			sleep 0.1
		done
	fi
	state_of "$dir"
}

cmd_list() {
	[[ $# -eq 0 ]] || usage
	[[ -d $ROOT ]] || return 0
	local entry
	for entry in "$ROOT"/*/; do
		[[ -d $entry ]] || continue
		entry=${entry%/}
		printf '%s\n' "${entry##*/}"
	done
}

[[ $# -ge 1 ]] || usage
verb=$1
shift
case $verb in
start) cmd_start "$@" ;;
status) cmd_status "$@" ;;
read) cmd_read "$@" ;;
kill) cmd_kill "$@" ;;
list) cmd_list "$@" ;;
*) usage ;;
esac
