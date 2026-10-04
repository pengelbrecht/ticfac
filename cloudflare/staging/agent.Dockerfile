# syntax=docker/dockerfile:1
# The staging AGENT image (epic 43y, tick xd3): the staging image plus git and
# a stand-in `ticks-worker` that honours the pinned --boot/--finish contract
# (contracts/worker-boot-contract.json) on a throwaway repository inside the
# container — a bare origin at /srv/origin.git, seeded on the first boot — so
# a WorkerAgent attempt runs end to end without the factory image, a GitHub
# repository or tk (src/staging-agent.ts says why).
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash coreutils procps util-linux ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
COPY proc.sh /usr/local/bin/ticks-proc
COPY <<'WORKER' /usr/local/bin/ticks-worker
#!/usr/bin/env bash
# The stand-in worker: the contract's markers and exit codes, nothing else.
set -uo pipefail
workdir="${TICKS_WORKDIR:-/work/repo}"
state_dir="${TICKS_WORKER_STATE_DIR:-/tmp/ticks-worker}"
tick="${TICKS_TICK:-xd3}"
branch="tick/proof/${tick}"
say() { printf 'ticks-worker: %s\n' "$*"; }
git config --global user.name "ticks sandbox"
git config --global user.email "ticks-sandbox@ticks.invalid"
git config --global init.defaultBranch main
case "${1:-}" in
--boot)
	if [ ! -d /srv/origin.git ]; then
		git init -q --bare /srv/origin.git || exit 3
		seed="$(mktemp -d)"
		git -C "$seed" init -q
		printf '#!/bin/sh\necho "Helo, world"\n' >"$seed/greet.sh"
		chmod +x "$seed/greet.sh"
		printf '# proof\n\nA throwaway repository for the WorkerAgent staging proof.\n' >"$seed/README.md"
		git -C "$seed" add -A && git -C "$seed" commit -q -m "seed" && git -C "$seed" push -q /srv/origin.git HEAD:main || exit 3
		say "seeded /srv/origin.git"
	fi
	rm -rf "$workdir" && git clone -q /srv/origin.git "$workdir" || exit 3
	git -C "$workdir" checkout -q -B "$branch" || exit 3
	mkdir -p "$state_dir" && printf '%s\n' "$branch" >"$state_dir/branch"
	say "cloned at $(git -C "$workdir" rev-parse --short HEAD); worker branch ${branch}"
	say "ticks-worker-boot-ok branch=${branch} result=RESULT-${tick}.md"
	printf '%s\n' "ticks-worker-boot-prompt-begin"
	printf '%s\n' "${TICKS_ROLE_PROMPT:-}"
	printf '%s\n' "ticks-worker-boot-prompt-end"
	exit 0
	;;
--finish)
	status="${2:-0}"
	cd "$workdir" || exit 3
	recorded="$(sed -n 1p "$state_dir/branch" 2>/dev/null)"
	[ -n "$recorded" ] || { say "no boot record"; exit 6; }
	report="RESULT-${tick}.md"
	git add -A && { git diff --cached --quiet || git commit -q -m "ticks-worker: report and remaining work"; }
	git push -q origin "HEAD:refs/heads/${recorded}" || { say "push failed"; exit 9; }
	say "pushed ${recorded} at $(git rev-parse --short HEAD) (harness status ${status})"
	[ -f "$report" ] || { say "no report at ${report}"; exit 11; }
	grep -q '^STATUS: ' "$report" || { say "the report has no STATUS line"; exit 11; }
	exit 0
	;;
*)
	say "the stand-in runs --boot and --finish only"
	exit 2
	;;
esac
WORKER
RUN chmod 0755 /usr/local/bin/ticks-proc /usr/local/bin/ticks-worker
