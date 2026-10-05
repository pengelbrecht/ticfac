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
# A head with no run is not always a missed one. ci.yml starts no run for a
# push that changes only run state and tracker records (its paths-ignore;
# ticks 5ob, ciw), so a `tick:` commit on main has no run by design and ships
# nothing. Such a head is left alone when the newest commit before it that DID
# get a run differs from it only under those paths: that run is the verdict on
# this code. Any other difference - a missed code commit under the tracker
# commits - is dispatched for, as before.
#
# Environment: GITHUB_REPOSITORY (owner/name) and a GH_TOKEN that may dispatch
# workflows. CI_WATCHDOG_GRACE_SECONDS (default 600) is how long a head may sit
# without a run before it counts as missed; CI_WATCHDOG_NOW (epoch seconds)
# stands in for the clock in tests.
set -euo pipefail

repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
grace="${CI_WATCHDOG_GRACE_SECONDS:-600}"
# The paths ci.yml's paths-ignore lists, as one anchored pattern. It must equal
# internal/reconcile's ciIgnoredPrefixes (TestTheWorkflowIgnoresExactlyTheCIIgnoredPaths).
ignored='^(\.ticfac/|\.tick/issues/|\.tick/activity/|\.tick/pending/)'
# How far back along main a commit with a run is looked for.
walk="${CI_WATCHDOG_WALK:-30}"
now="${CI_WATCHDOG_NOW:-$(date +%s)}"

head="$(gh api "repos/${repo}/commits/main" --jq .sha)"
committed="$(gh api "repos/${repo}/commits/${head}" --jq '.commit.committer.date | fromdateiso8601')"
age=$((now - committed))

# A run that was cancelled is no verdict: deploy-factory only ships a CI run
# that succeeded. Queued, running, failed and succeeded runs all mean CI saw
# the commit, and a failure is CI's to report, not this script's to retry.
ci_runs() {
	gh api "repos/${repo}/actions/workflows/ci.yml/runs?head_sha=$1&per_page=100" \
		--jq '[.workflow_runs[] | select(.conclusion != "cancelled")] | length'
}
runs="$(ci_runs "$head")"

if ((runs > 0)); then
	echo "main ${head} has CI (${runs} run(s)); nothing to do"
	exit 0
fi
if ((age < grace)); then
	echo "main ${head} has no CI run yet, ${age}s after it landed; a push's run may still be on its way (grace ${grace}s)"
	exit 0
fi

# Is the head a commit CI ignores by design? Find the newest commit before it
# with a run, and ask whether everything since lives under the ignored paths.
for sha in $(gh api "repos/${repo}/commits?sha=${head}&per_page=${walk}" --jq '.[].sha'); do
	[ "$sha" = "$head" ] && continue
	if (($(ci_runs "$sha") > 0)); then
		changed="$(gh api "repos/${repo}/compare/${sha}...${head}" --jq '.files[].filename')"
		code="$(grep -Ev "$ignored" <<<"$changed" || true)"
		if [ -z "$code" ]; then
			echo "main ${head} has no CI run, by design: everything since ${sha}, which has one, is run state or tracker records (ci.yml's paths-ignore)"
			exit 0
		fi
		break
	fi
done
echo "main ${head} has no CI run ${age}s after it landed: GitHub never started one, so nothing would deploy it. Dispatching CI on main."
gh workflow run ci.yml --repo "${repo}" --ref main
