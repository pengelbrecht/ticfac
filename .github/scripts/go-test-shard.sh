#!/usr/bin/env bash
# go-test-shard.sh PKGS SHARD OF — one job of ci.yml's sharded Go suite.
#
# OF=0: run every test in PKGS (space-separated package patterns).
# OF>0: run shard SHARD (1-based) of OF of the single package PKGS: its
#       top-level tests, sorted by name, dealt round-robin, so the OF shards
#       partition the package by construction — a new test lands in exactly
#       one shard without anyone listing it.
#
# Both go through `make test` so the timeout and -parallel stay the
# Makefile's (GOTEST_RUN narrows it to this shard's tests).
set -euo pipefail

pkgs="$1"
shard="${2:-0}"
of="${3:-0}"

if [ "$of" -eq 0 ]; then
	# shellcheck disable=SC2086 # PKGS is a list of patterns
	exec make test PKGS="$pkgs"
fi

mapfile -t all < <(go test -list '.*' "$pkgs" | grep -E '^(Test|Example|Fuzz)' | sort)
mine=()
for i in "${!all[@]}"; do
	if [ $((i % of)) -eq $((shard - 1)) ]; then mine+=("${all[$i]}"); fi
done
echo "shard $shard/$of of $pkgs: ${#mine[@]} of ${#all[@]} tests" >&2
if [ "${#mine[@]}" -eq 0 ]; then
	echo "nothing in this shard" >&2
	exit 0
fi
GOTEST_RUN="^($(
	IFS='|'
	echo "${mine[*]}"
))\$" exec make test PKGS="$pkgs"
