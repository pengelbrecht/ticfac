#!/usr/bin/env bash
# Starts CI on main's head when GitHub never did.
#
# Deploys hang off main's CI (deploy-factory.yml runs on CI's workflow_run), so
# a main commit with no CI run is a fix that silently never ships. On
# 2026-10-01 the squash merge of #178 (03f56af4) landed on main and GitHub
# never emitted its push event: no PushEvent in the repository's event feed,
# no CI run of any status, so nothing deployed until a person noticed and
# dispatched CI by hand. Nothing in the repository caused it (no paths filter
# matched, and a run cancelled by concurrency still shows as a run), so the
# answer is a check rather than a trigger: this script, run on a schedule
# (ci-watchdog.yml), dispatches CI on main when main's head has no CI run that
# was not cancelled, once the head has been there longer than a push's run
# takes to appear.
#
# Environment: GITHUB_REPOSITORY (owner/name) and a GH_TOKEN that may dispatch
# workflows. CI_WATCHDOG_GRACE_SECONDS (default 600) is how long a head may sit
# without a run before it counts as missed; CI_WATCHDOG_NOW (epoch seconds)
# stands in for the clock in tests.
set -euo pipefail

repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
grace="${CI_WATCHDOG_GRACE_SECONDS:-600}"
now="${CI_WATCHDOG_NOW:-$(date +%s)}"

head="$(gh api "repos/${repo}/commits/main" --jq .sha)"
committed="$(gh api "repos/${repo}/commits/${head}" --jq '.commit.committer.date | fromdateiso8601')"
age=$((now - committed))

# A run that was cancelled is no verdict: deploy-factory only ships a CI run
# that succeeded. Queued, running, failed and succeeded runs all mean CI saw
# the commit, and a failure is CI's to report, not this script's to retry.
runs="$(gh api "repos/${repo}/actions/workflows/ci.yml/runs?head_sha=${head}&per_page=100" \
	--jq '[.workflow_runs[] | select(.conclusion != "cancelled")] | length')"

if ((runs > 0)); then
	echo "main ${head} has CI (${runs} run(s)); nothing to do"
	exit 0
fi
if ((age < grace)); then
	echo "main ${head} has no CI run yet, ${age}s after it landed; a push's run may still be on its way (grace ${grace}s)"
	exit 0
fi
echo "main ${head} has no CI run ${age}s after it landed: GitHub never started one, so nothing would deploy it. Dispatching CI on main."
gh workflow run ci.yml --repo "${repo}" --ref main
