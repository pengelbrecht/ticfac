package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The deploy half of CI (SPEC §12 Phase 4 item 7, tick vyg).
//
// The factory's credentials, embedded payload and deployment bundles moved to
// this repository with ticks ticks ek7/b3a and the Phase 4 move (l9n); the one
// half of item 7 that did not exist ANYWHERE was a release deploy driven by
// this repository's own CI. Until it exists, "the deploy path lives here" is
// true of the code and false of the cadence: an operator's only upgrade paths
// are by hand, and the old repository's release story is the one the operator
// still reads about (ticks s70 is that story's open secret-configuration
// work).
//
// The workflow these tests pin is deliberately an ordinary release consumer
// of `ticfac factory deploy` — the same installer an operator runs by hand,
// against the bundle embedded in the exact commit being released — because a
// CI deploy that reached around the installer (a hand-rolled `wrangler deploy`
// against a copy of the tree, say) would be a second deploy path, and the
// whole point of item 7 is that there is one.

// deployWorkflowPath is the factory's release deploy workflow, pinned at the
// repository root where CI reads it.
const deployWorkflowPath = ".github/workflows/deploy-factory.yml"

// readDeployWorkflow returns the deploy workflow's bytes. It is read through
// contracts.RepoRoot because the file is a repository artifact, not a
// package one.
func readDeployWorkflow(t *testing.T) string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, deployWorkflowPath))
	if err != nil {
		t.Fatalf("reading %s: %v\n(the factory's release deploy moved here with the deploy path; a repository whose CI does not deploy its own deployable has left the by-hand path as the only live one)", deployWorkflowPath, err)
	}
	return string(data)
}

// contains reports whether the workflow's text contains want, with the
// assertion message a reviewer reads when it does not.
func workflowMustContain(t *testing.T, workflow, want, because string) {
	t.Helper()
	if !strings.Contains(workflow, want) {
		t.Errorf("the deploy workflow does not contain %q — %s", want, because)
	}
}

func workflowMustNotContain(t *testing.T, workflow, want, because string) {
	t.Helper()
	if strings.Contains(workflow, want) {
		t.Errorf("the deploy workflow contains %q — %s", want, because)
	}
}

// TestCIDeploysTheFactoryFromTicfac pins the shape that makes item 7's "CI
// deploys from ticfac" true and the old path replaceable:
//
//   - releases upgrade the factory (a v* tag triggers the workflow), and the
//     workflow can also be dispatched by hand — the operator's own upgrade
//     without cutting a tag;
//   - the deploy runs THE INSTALLER (`ticfac factory deploy`) from a binary
//     built out of THIS commit's tree — not wrangler against a copy, not a
//     release artifact of some other repository;
//   - deploys serialize (one concurrency group): two tag pushes racing one
//     Cloudflare account is the two-writers-one-name problem Phase 4's
//     Durable Object item exists to close on the cloud host, and it is no
//     more welcome in the release cadence.
func TestCIDeploysTheFactoryFromTicfac(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, "tags:",
		"a v* tag is the release cadence that upgrades the factory")
	workflowMustContain(t, workflow, `- "v*"`,
		"the release trigger must be a version tag, not every push")
	workflowMustContain(t, workflow, "workflow_dispatch:",
		"the operator must be able to deploy a head without cutting a tag")
	workflowMustContain(t, workflow, "concurrency:",
		"two tag pushes must not race one deploy")

	workflowMustContain(t, workflow, "contents: read",
		"a public repository's deploy workflow takes the least privilege")
	workflowMustNotContain(t, workflow, "pengelbrecht/ticks",
		"the deploy depends on ticks' deployment path, which item 7 exists to replace; ticks is a dependency through tk and the pinned bundle, never a checkout or a workflow the release shells into")

	// The binary the workflow deploys with is built from this commit.
	workflowMustContain(t, workflow, "./cmd/ticfac",
		"the deploy must use a ticfac built out of the released tree — the embedded bundle IS the version pin (D16)")
	// And what it runs is the installer, exactly once, with no flags: the
	// exact string pins the no-rotation contract below too. (Its output is
	// piped on — kept for the job summary, filtered for the public log — so
	// the invocation is matched as a line, not as a `run:` value.)
	var invocations []string
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "run:"))
		if strings.HasPrefix(trimmed, "./ticfac factory deploy") {
			invocations = append(invocations, trimmed)
		}
	}
	if len(invocations) != 1 {
		t.Fatalf("the workflow invokes `./ticfac factory deploy` %d times, want exactly once — the installer, not a second deploy path grown around it", len(invocations))
	}
	if rest := strings.TrimPrefix(invocations[0], "./ticfac factory deploy"); strings.HasPrefix(strings.TrimSpace(rest), "-") {
		t.Errorf("the deploy runs with flags (%q) — CI runs the installer with none", invocations[0])
	}
}

// TestCIDeploysMainOnlyAfterCIPassed pins the operator's 2026-09-29 decision:
// GitHub Actions is the normal way the factory is deployed, so main keeps it
// current — but only a commit CI passed, one deploy at a time, never taking a
// live run's containers (the rollout grace period, not a wait), and every
// deploy leaves a record of what it shipped where the operator looks.
func TestCIDeploysMainOnlyAfterCIPassed(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, "workflow_run:",
		"main deploys once CI has concluded, from the verdict CI reached — not on the push, before CI has said anything")
	workflowMustContain(t, workflow, "workflows: [CI]",
		"the deploy waits on ci.yml (whose name is CI), not on any workflow")
	workflowMustContain(t, workflow, "github.event.workflow_run.conclusion == 'success'",
		"a failed or cancelled CI must never deploy")
	workflowMustContain(t, workflow, "github.event.workflow_run.head_branch == 'main'",
		"only main deploys; an epic branch's CI passing is not a release")
	workflowMustContain(t, workflow, "github.event.workflow_run.event == 'workflow_dispatch'",
		"a dispatched CI run on main cancels the push's run (ci.yml's concurrency), so its verdict must deploy too — or main's head may never deploy")
	workflowMustContain(t, workflow, "github.event.workflow_run.head_sha",
		"the deploy must check out the commit CI tested, not main's head at the time the event fired")
	workflowMustContain(t, workflow, "cancel-in-progress: false",
		"a newer commit may replace a queued deploy, but must never tear down one mid-push")
	workflowMustContain(t, workflow, "git diff --name-only",
		"a main commit that changes nothing the factory ships must not redeploy it")
	workflowMustContain(t, workflow, "factory_deployment",
		"the path check diffs against the commit the factory actually runs, so a superseded or skipped deploy is not lost")

	// No live-run wait: the rollout grace period is what keeps a deploy off a
	// live run's containers, and the deploy names the runs holding the old
	// image instead. The guard that waited up to an hour checked once, before
	// a rollout that outlived it, and could not see --cloud-workers runs.
	workflowMustNotContain(t, workflow, "ticfac cloud status --json",
		"the deploy must not wait on live runs: rollout_active_grace_period protects their containers, and the wait only delayed deploys")
	workflowMustContain(t, workflow, "rollout_active_grace_period",
		"the workflow must say what protects a live run now that it does not wait for one")
	workflowMustContain(t, workflow, "held by live run",
		"the summary must name the runs still holding instances on the previous image")
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	toml, err := os.ReadFile(filepath.Join(root, "cloudflare", "wrangler.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toml), "\nrollout_active_grace_period = 86400\n") {
		t.Error("cloudflare/wrangler.toml no longer sets rollout_active_grace_period = 86400 — the deploy workflow " +
			"deploys under live runs on the strength of it; restore it or restore a live-run wait")
	}
	workflowMustNotContain(t, workflow, "WRANGLER_DOCKER_BIN:",
		"the deploy sets wrangler's docker to the bundle's shim itself (dockershim.go); a workflow override would bypass it")
	workflowMustContain(t, workflow, "TICFAC_BUILDX_BUILDER:",
		"CI's image build goes through buildx with the Actions cache, which the shim switches on from this")
	workflowMustContain(t, workflow, "sed -u -E",
		"the public-log filter must be line-buffered, or every line of a deploy carries the timestamp of its end")

	workflowMustContain(t, workflow, "GITHUB_STEP_SUMMARY",
		"the job summary records what was deployed")
	for _, fact := range []string{"Worker version", "image digest", "commit"} {
		workflowMustContain(t, workflow, fact, "the job summary must name the deployment's "+fact)
	}

	workflowMustContain(t, workflow, "::add-mask::",
		"this repository is public: the factory's endpoint is masked out of the deploy's log")
}

// TestTheDeployCISkipsLoudlyOnMissingSecrets is ticks s70's lesson, pinned so
// it cannot be relearned here. s70's release run skipped its cloud deploy
// because the secrets were not configured, and the skip was found by
// inspection — nothing in the run SAID anything. This workflow must do better
// in both directions: the skip names every secret it lacks as a warning
// annotation (visible in the release's own page), and it is a skip, not a
// failure — the factory is the operator's opt-in (D16), so a fork or a
// contributor with no Cloudflare account must not see red releases.
func TestTheDeployCISkipsLoudlyOnMissingSecrets(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, "::warning::",
		"a skipped deploy must announce itself in the run's annotations, not only in a step log read by inspection (s70)")
	for _, secret := range []string{
		"CLOUDFLARE_API_TOKEN",
		"CLOUDFLARE_ACCOUNT_ID",
		"TICFAC_FACTORY_TOKEN",
	} {
		workflowMustContain(t, workflow, secret,
			"the skip must name every secret the deploy needs, so the operator configures all of them, not the two they guessed")
	}
	// The skip gates the steps rather than failing the job: the workflow
	// stays green without the secrets, and every later step keys on the
	// one output.
	workflowMustContain(t, workflow, "configured == 'true'",
		"steps must run only when the credential check passed")
	workflowMustContain(t, workflow, `>> "$GITHUB_OUTPUT"`,
		"the check must hand its verdict to the later steps through the step output")
}

// TestTheDeployCIReusesTheOperatorsToken pins the one rule that keeps the
// factory's bearer token a credential of the DEPLOYMENT rather than of one
// machine (the tick's own words: "do not let a secret become a thing that only
// works on one machine"; the Phase 3 split, ticks 9hu, is the precedent).
//
// `ticfac factory deploy` reuses a stored token and never mints one when the
// credential file holds any. A CI runner has no ~/.ticfacrc, so an unmitigated
// deploy would mint a fresh token, push its hash to the Worker — invalidating
// the operator's laptop token at the same moment — and write the new token to
// the runner's ephemeral disk, where it dies with the job. Every party would
// hold a token that no longer works. The workflow therefore stages the token
// from a repository secret into the credential file the deploy reads, and the
// deploy reuses it.
func TestTheDeployCIReusesTheOperatorsToken(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, ".ticfacrc",
		"the deploy must find the operator's token in the factory's own credential file, where `ticfac factory deploy` already looks for it")
	workflowMustContain(t, workflow, "factory_token=",
		"the staged credential file must carry the token under the file's real key, not an invented one")
	workflowMustContain(t, workflow, "umask 077",
		"the staged credential file must be owner-only; the file's format itself is 0600 everywhere else")
	workflowMustNotContain(t, workflow, "--rotate-token",
		"the CI deploy must never rotate the token: a rotated token is written to the runner's ephemeral home and lost, and the operator's laptop token dies with the same push")
}

// TestTheDeployCIStatesCredentialOwnership pins the ownership statement the
// tick asks for: which credentials are ticfac's (the factory's own file and
// its Worker-secret mirrors, walked and verified by `ticfac factory setup`),
// which remain the operator's, and what CI holds as a MIRROR of the operator's
// so a deploy from CI and a deploy from the laptop are the same identity.
//
// The statement lives in the workflow's comments because that is where a
// release reviewer reads it: the secrets block is the one place the cadence's
// credentials are all named, and the mirror rule ("the repository secret and
// ~/.ticfacrc must hold the SAME token") is the one that keeps a stale mirror
// from re-pushing an old token's hash and stranding the operator's newer one.
func TestTheDeployCIStatesCredentialOwnership(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, "Credential ownership",
		"the ownership statement must be there to read, not implied by which secrets happen to be named")
	workflowMustContain(t, workflow, "~/.ticfacrc",
		"the statement must name the factory's own credential file — the Phase 3 split (ticks 9hu) is the precedent it follows")
	workflowMustContain(t, workflow, "ticfac factory setup",
		"the statement must name the ladder that walks and verifies the rungs")
	workflowMustContain(t, workflow, "ticfac factory status",
		"the statement must name the status surface that re-checks every rung live")
	workflowMustContain(t, workflow, "same token",
		"the mirror rule must be stated: the repository secret and ~/.ticfacrc must hold the same token, or a stale mirror strands the laptop's newer one")
	workflowMustContain(t, workflow, "refresh token",
		"the statement must say the GitHub refresh token stays on the operator's machine (D11): it is deliberately NOT mirrored into CI")
}

// TestTheDeployCIRecordsItsTicksDeferrals pins the two pieces of open work the
// tick names — s70 and zkq, both in the ticks tracker — as deferrals with
// reasons, recorded in the workflow itself so the next reader does not have to
// recover them from a run report. The acceptance criterion is "resolved or
// explicitly deferred with a reason"; these are the reasons.
func TestTheDeployCIRecordsItsTicksDeferrals(t *testing.T) {
	t.Parallel()
	workflow := readDeployWorkflow(t)

	workflowMustContain(t, workflow, "s70",
		"the s70 deferral must be recorded where its lesson is applied: this workflow's skip is the fix for the class of failure s70 filed")
	workflowMustContain(t, workflow, "zkq",
		"the zkq deferral must be recorded: the factory's own required-tk-commands is a separate list, and the structural gap zkq names belongs to the ticks-owned tree it is filed against")
}
