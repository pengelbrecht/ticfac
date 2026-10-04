#!/usr/bin/env bash
# ci-plan.sh — what CI runs for this event (ci.yml's `plan` job).
#
# Two modes:
#
#   full      push (main, epic/**) and workflow_dispatch. Every Go package,
#             internal/reconcile split across RECONCILE_SHARDS jobs, the race
#             suite over ./..., and the TypeScript job. Main's full run is the
#             deploy gate (deploy-factory.yml runs on its success).
#   affected  pull_request. Only the Go packages the diff can change the
#             verdict of: the packages whose files changed, plus every package
#             whose test binary depends on one of them. The Go jobs are skipped
#             when that set is empty, and typescript when nothing it reads
#             changed. Advisory, and fast: agents merge on local targeted tests
#             and `make gate`, and main's full run catches what this misses.
#
# A pull request goes FULL anyway when it changes what every package's
# verdict rests on: go.mod/go.sum, the Makefile (CI's and the gate's recipes,
# read by drift-guard tests), internal/shorttest (the whole-repo guard), CI
# itself (.github/), or a file outside any Go package that the root package
# does not embed and that is not plain documentation — a file some test reads
# by path, whose readers no import graph can name.
#
# Inputs: the PR's changed paths on stdin, one per line (affected mode only);
# env EVENT (github.event_name), RECONCILE_SHARDS (default 3), GITHUB_OUTPUT
# (where the outputs go; stdout when unset). Outputs: mode, go, ts,
# race_pkgs, matrix.
set -euo pipefail

event="${EVENT:?EVENT is required}"
shards="${RECONCILE_SHARDS:-3}"
out="${GITHUB_OUTPUT:-/dev/stdout}"
module="$(go list -m)"
reconcile="$module/internal/reconcile"

mode=full
reason="$event: the full suite"
changed_pkgs=()
ts=false

if [ "$event" = "pull_request" ]; then
	mode=affected
	mapfile -t files # the diff's paths, one per line, on stdin
	# Every package's directory, relative to the repository root, and the
	# root package's embedded files (image/, cloudflare/src, contracts/…).
	declare -A pkgdir=()
	while read -r imp dir; do
		rel="${dir#"$PWD"}"
		rel="${rel#/}"
		pkgdir["${rel:-.}"]="$imp"
	done < <(go list -f '{{.ImportPath}} {{.Dir}}' ./...)
	declare -A embedded=()
	while read -r f; do embedded["$f"]=1; done < <(go list -f '{{range .EmbedFiles}}{{println .}}{{end}}' "$module")

	for f in "${files[@]}"; do
		case "$f" in
		cloudflare/* | contracts/* | contracts.pin.json | harness/*) ts=true ;;
		esac
		case "$f" in
		go.mod | go.sum | Makefile | internal/shorttest/* | .github/* | .tick/runners.toml)
			mode=full
			reason="the diff changes $f, which every package's verdict rests on"
			break
			;;
		esac
		if [ -n "${embedded[$f]:-}" ]; then
			changed_pkgs+=("$module")
			continue
		fi
		# The nearest Go package at or above the file (testdata/ and
		# non-package subdirectories belong to the package that reads them).
		d="$(dirname "$f")"
		while [ "$d" != "." ] && [ -z "${pkgdir[$d]:-}" ]; do d="$(dirname "$d")"; done
		if [ "$d" != "." ]; then
			changed_pkgs+=("${pkgdir[$d]}")
			continue
		fi
		case "$f" in
		*.go) changed_pkgs+=("$module") ;;
		# image/ is the sandbox image internal/sandboximage runs for real;
		# install.sh and the release config are internal/release's subject.
		image/*) changed_pkgs+=("$module" "$module/internal/sandboximage") ;;
		install.sh | .goreleaser.yaml) changed_pkgs+=("$module/internal/release") ;;
		# The worker's own sources and tests: typescript's, not Go's (what Go
		# embeds of cloudflare/ was matched above).
		cloudflare/*) ;;
		# The pi-durable harness package: its conformance and replay suites run
		# in the typescript job; no Go verdict rests on it.
		harness/*) ;;
		# Run records, the tracker, documentation: no Go verdict rests on them.
		*.md | .ticfac/* | .tick/* | .gitignore | .gitattributes | benchmarks/*) ;;
		*)
			mode=full
			reason="the diff changes $f, which no Go package owns: a test may read it by path"
			break
			;;
		esac
	done
fi

if [ "$mode" = full ]; then
	ts=true
	mapfile -t affected < <(go list ./...)
else
	# Every package whose test binary depends on a changed package (or is one).
	declare -A hit=()
	for p in "${changed_pkgs[@]}"; do hit["$p"]=1; done
	declare -A aff=()
	if [ "${#hit[@]}" -gt 0 ]; then
		while IFS='|' read -r name deps; do
			name="${name%% *}"
			name="${name%.test}"
			if [ -n "${hit[$name]:-}" ]; then
				aff["$name"]=1
				continue
			fi
			for dep in $deps; do
				if [ -n "${hit[$dep]:-}" ]; then
					aff["$name"]=1
					break
				fi
			done
		done < <(go list -test -f '{{.ImportPath}}|{{join .Deps " "}}' ./...)
	fi
	mapfile -t affected < <(printf '%s\n' "${!aff[@]}" | grep . | sort || true)
	reason="pull request: ${#affected[@]} affected package(s)"
fi

go=false
[ "${#affected[@]}" -gt 0 ] && go=true

# The test matrix: internal/reconcile in $shards shards when it is affected,
# every other affected package in one job.
rest=()
has_reconcile=false
for p in "${affected[@]}"; do
	if [ "$p" = "$reconcile" ]; then has_reconcile=true; else rest+=("./${p#"$module"/}"); fi
done
# The root package's import path has no "/" suffix to strip.
for i in "${!rest[@]}"; do [ "${rest[$i]}" = "./$module" ] && rest[i]="."; done

matrix='[]'
if [ "$has_reconcile" = true ]; then
	for ((s = 1; s <= shards; s++)); do
		matrix="$(jq -c --arg s "$s" --arg n "$shards" \
			'. + [{name: "reconcile \($s)/\($n)", pkgs: "./internal/reconcile", shard: ($s|tonumber), of: ($n|tonumber), node: false}]' <<<"$matrix")"
	done
fi
if [ "${#rest[@]}" -gt 0 ]; then
	node=false
	# The pi-durable local host (tick hpk) runs on node and the pinned
	# @earendil-works packages: internal/exec/subprocess's end-to-end test
	# spawns the real harness, so it skips itself wherever harness/node_modules
	# is not installed — the same reason cloudflaresandbox sets node.
	case " ${rest[*]} " in *" ./internal/exec/cloudflaresandbox "*|*" ./internal/exec/subprocess "*) node=true ;; esac
	matrix="$(jq -c --arg p "${rest[*]}" --argjson node "$node" \
		'. + [{name: "packages", pkgs: $p, shard: 0, of: 0, node: $node}]' <<<"$matrix")"
fi

race_pkgs="./..."
if [ "$mode" = affected ]; then
	race_pkgs=""
	for p in "${affected[@]}"; do
		if [ "$p" = "$module" ]; then race_pkgs+=". "; else race_pkgs+="./${p#"$module"/} "; fi
	done
fi

echo "plan: $mode — $reason" >&2
echo "go=$go ts=$ts" >&2
printf '  %s\n' "${affected[@]}" >&2
{
	echo "mode=$mode"
	echo "go=$go"
	echo "ts=$ts"
	echo "race_pkgs=${race_pkgs% }"
	echo "matrix={\"include\":$matrix}"
} >>"$out"
