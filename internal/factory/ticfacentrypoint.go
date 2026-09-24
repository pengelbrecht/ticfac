package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The orchestrator container runs ticfac, not a harness on a skill loop
// (tick hn0).
//
// WHAT CHANGES, AND WHAT DELIBERATELY DOES NOT.
// /usr/local/bin/ticks-orchestrator clones the repo at the submitted SHA,
// verifies tk, adopts the run branch, provisions the toolchain, runs the
// repository's `[sandbox]` setup and its `[environment.commands]` pre-flight,
// exports TK_ACTOR=cloud:orchestrator, starts the RUN KEEPER — and then execs
// a headless harness on the ticks skill loop, which is a MODEL deciding
// control flow. Only that last step changes. Everything above it is kept,
// the keeper most of all: it pushes the run branch as soon as there is
// anything on it and heartbeats on a timer, which is why committed work
// already outlives the container (epic yoh's eviction story rests on it).
//
// WHY THE DEPLOY REWRITES THE STAGED ENTRYPOINT.
// Three mechanisms were available and two of them are closed:
//
//   - The entrypoint cannot be told to exec something else. `start_harness`
//     builds its command from a closed `case "$harness"` over exactly `omp`
//     and `claude`, and TICKS_HARNESS chooses between those two harnesses —
//     both models. There is no env var or argument that selects a
//     deterministic orchestrator, so nothing can be configured here.
//
//   - The change cannot go upstream into ticks for the same reason the ticfac
//     binaries could not (ticfacbin.go): ticks' own build context holds no
//     ticfac, so a ticks image that booted `ticfac run-epic` would be a ticks
//     image that cannot boot at all.
//
//   - So it goes where the deploy's other image edits already go. image/ is
//     ticfac's VENDORED copy of ticks' cloud/sandbox tree, and sandbox.pin.json
//     states the rule outright: "ticfac never edits a file under image/". CI
//     enforces it from both ends (`go run ./cmd/sandbox check` and
//     `verify-upstream`). The STAGED copy is a different thing: SetSandboxTkPins
//     rewrites the staged Dockerfile's pins, SetSandboxTicfacPins inserts
//     ticfac's, and MaterializeSandbox rewrites the whole staged tree from the
//     embedded one on every deploy — so an edit there cannot accumulate and the
//     vendored bytes are never touched. This is that, applied to entrypoint.sh.
//
// WHY IT WRAPS THE FUNCTION RATHER THAN PATCHING ITS BODY.
// The inserted block is appended immediately before the script's final
// `main "$@"`, and it REDEFINES `start_harness`. Not one existing line is
// rewritten: bash takes the last definition of a function, and `main` is
// called after both. The original is kept under another name — captured with
// `declare -f`, so the capture depends on no text in it — because one phase
// must still reach it (see below). A rewrite that edited the body would have
// to match the vendored script's prose, and would break the next time ticks
// changed a word of it.
//
// WHY `review` IS NOT REDIRECTED.
// A review boot reads a hostile pull request and writes prose; it holds no
// push credential, commits nothing, opens no PR, and the only thing it
// produces is a findings file the entrypoint itself posts. There is no epic to
// reconcile, so it keeps the harness it has always had.

// ticfacEntrypointName is the orchestrator's run entrypoint inside the staged
// image context. The Dockerfile installs it as /usr/local/bin/ticks-orchestrator.
const ticfacEntrypointName = "entrypoint.sh"

// entrypointReDerive is the remedy every anchor refusal ends with. One
// sentence, because all three failures have the same fix and the same owner.
const entrypointReDerive = "the vendored entrypoint changed shape under sandbox.pin.json " +
	"and this rewrite has to be re-derived against it (internal/factory/ticfacentrypoint.go). " +
	"The deploy stops here on purpose: an override that does not apply is a container that boots " +
	"a model on the skill loop, which is what tick hn0 removed."

// ticfacEntrypointMarker identifies the inserted block, so a second
// application is a refusal rather than two definitions of one function.
const ticfacEntrypointMarker = "# >>> ticfac run-epic (tick hn0)"

// ticfacEntrypointCloseMarker ends the inserted block. A constant rather
// than a second line of the raw string below so the tests can extract the
// block from the STAGED script by its markers and run exactly what the
// image ships — finding 7be27471: proven through the staged script rather
// than by reading it.
const ticfacEntrypointCloseMarker = "# <<< ticfac run-epic (tick hn0)"

// THE THREE ANCHORS, AND WHY EACH ONE IS A STOP.
//
// This rewrite is coupled to three structural facts about a file ticfac does
// not own and that gets vendor-bumped with sandbox.pin.json. Each is checked,
// and a missing one FAILS THE DEPLOY. The outcome being guarded against is
// specific and quiet: a rewrite that still produces a script bash accepts —
// `bash -n` green, the image builds, the container boots — but whose override
// is never reached, so a model orchestrates again. That is the exact thing
// this tick removes, and it would be invisible until somebody read a run log.
//
//   - `main "$@"` is the last line. The block is inserted BEFORE it, because a
//     function defined after the call that uses it is a function bash never
//     sees. No anchor, no insertion point.
//
//   - a function named `start_harness` is defined. It is what `declare -f`
//     captures for the review phase and what the block redefines.
//
//   - `main` CALLS it by that name. This is the one that matters most: if ticks
//     renames the call site, the block still parses, still defines
//     `start_harness`, and nothing ever calls it. Every other failure is loud
//     on its own; this one is not.
//
// When one of these fires, the rewrite has to be re-derived against the new
// vendored script — which is a person's job, at the moment the pin moves.
var (
	entrypointMainPattern = regexp.MustCompile(`(?m)^main "\$@"[ \t]*$`)

	entrypointHarnessDefPattern = regexp.MustCompile(`(?m)^start_harness\(\)[ \t]*\{[ \t]*$`)

	entrypointHarnessCallPattern = regexp.MustCompile(`(?m)^[ \t]+start_harness[ \t]*$`)
)

// SetSandboxOrchestratorEntrypoint rewrites the STAGED orchestrator entrypoint
// so that a run boot execs `ticfac run-epic` instead of a headless harness.
//
// Same contract as SetSandboxTkPins and SetSandboxTicfacPins: the staged copy
// is edited, the vendored tree is not, and it must run after MaterializeSandbox
// — which writes the staged copy fresh — or it would be appending to a file
// about to be overwritten.
func SetSandboxOrchestratorEntrypoint(dir string) error {
	path := filepath.Join(dir, ticfacEntrypointName)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	text := string(data)
	if strings.Contains(text, ticfacEntrypointMarker) {
		return fmt.Errorf("%s already execs ticfac — the staged entrypoint is written fresh by MaterializeSandbox on every deploy, so a second insertion means the staging order is wrong", path)
	}
	anchor := entrypointMainPattern.FindStringIndex(text)
	if anchor == nil {
		return fmt.Errorf(`%s has no final "main \"$@\"" line to insert the ticfac override before — %s`, path, entrypointReDerive)
	}
	def := entrypointHarnessDefPattern.FindStringIndex(text)
	if def == nil {
		return fmt.Errorf("%s defines no start_harness() function for the ticfac override to replace — %s", path, entrypointReDerive)
	}
	if def[0] > anchor[0] {
		return fmt.Errorf("%s defines start_harness() AFTER its own `main \"$@\"`, which bash never reaches — %s", path, entrypointReDerive)
	}
	// The quiet one. A script that defines start_harness and never calls it by
	// that name would take the override, parse, build, boot — and run the
	// harness, because nothing reaches the redefinition.
	if !entrypointHarnessCallPattern.MatchString(text) {
		return fmt.Errorf("%s never calls start_harness by name, so the ticfac override would be defined and never reached: the container would boot a model on the skill loop and say nothing about it — %s", path, entrypointReDerive)
	}

	var b strings.Builder
	b.WriteString(text[:anchor[0]])
	b.WriteString(ticfacEntrypointBlock)
	b.WriteString("\n")
	b.WriteString(text[anchor[0]:])
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ticfacEntrypointBlock is what the deploy inserts. Read it as the whole of
// the change: everything the entrypoint did before `start_harness` is
// untouched, `start_harness` itself is replaced by a boot of `ticfac run-epic`,
// and the run keeper is started exactly where it was.
const ticfacEntrypointBlock = ticfacEntrypointMarker + `
# The orchestrator is a DETERMINISTIC reconciler, not a model following a skill
# loop. Inserted into the STAGED copy of this script by ` + "`ticfac factory deploy`" + `
# (internal/factory/ticfacentrypoint.go); it is not in the committed
# image/entrypoint.sh and cannot be — image/ is ticfac's vendored copy of ticks'
# cloud/sandbox tree, which ticfac never edits.
#
# Everything main() does before start_harness is unchanged: the clone at the
# submitted SHA, the run branch, tk's verification, toolchain provisioning, the
# repository's own setup and [environment.commands] pre-flight, the model and
# harness probes, and TK_ACTOR. Only the exec changes.

# The harness path, kept under its own name so the review phase can still reach
# it. Captured from the live definition rather than copied, so it cannot drift
# from the script above.
eval "ticks_harness_start_harness() $(declare -f start_harness | tail -n +2)"

# The default branch of the remote, as a BRANCH NAME.
#
# ` + "`ticfac run-epic --base`" + ` defaults to the literal string "HEAD", which is not
# a branch: base refresh resolves nothing and silently does nothing every round,
# and the epic PR falls back to a guessed base (tick udu). The clone here is a
# fetch of one SHA into a fresh ` + "`git init`" + `, so it has no origin/HEAD to read —
# the remote is asked instead, and only if that says nothing does this fall back
# to the same convention the reconciler documents.
ticfac_base_branch() {
	local ref
	ref="$(git -C "$workdir" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null)"
	if [[ -n $ref ]]; then
		printf '%s\n' "${ref#origin/}"
		return 0
	fi
	ref="$(git -C "$workdir" ls-remote --symref origin HEAD 2>/dev/null | awk '$1 == "ref:" { print $2; exit }')"
	if [[ -n $ref ]]; then
		printf '%s\n' "${ref#refs/heads/}"
		return 0
	fi
	warn "the remote named no default branch, so the epic is based on 'main' by convention"
	printf 'main\n'
}

start_harness() {
	# A review reads a pull request and writes prose. There is no epic to
	# reconcile, it holds no push credential, and the findings file this script
	# posts is the only thing it produces — so it keeps the harness.
	if [[ $phase == "review" ]]; then
		ticks_harness_start_harness
		return
	fi

	export TK_ACTOR="$ACTOR"
	export TICKS_RUN_ID="$run_id"
	export TICKS_PHASE="$phase"
	export TICKS_RUN_BRANCH="$run_branch"
	if [[ -n $run_pass ]]; then export TICKS_PASS="$run_pass"; fi
	if [[ -n $factory_url ]]; then export TICKS_FACTORY_URL="$factory_url"; fi
	if [[ -n $factory_token ]]; then export TICKS_FACTORY_TOKEN="$factory_token"; fi
	if [[ -n $factory_project ]]; then export TICKS_FACTORY_PROJECT="$factory_project"; fi

	# The substrate this run executes on (tick 84z), stated the way every
	# reader of the substrate honours: the override, never a rewrite of the
	# tracked config the run's workers commit against. This container IS the
	# cloud — role routing resolves against it, the target repository's
	# .tick/runners.cloud.toml role cells apply last, over .tick/runners.toml,
	# to every role including review and close-out, and a role nobody declared cloud
	# routing for REFUSES the run at start, naming the role — never a silent
	# fall back to the base cell, which is how a container once reached a
	# claude process nobody chose.
	export TICKS_SUBSTRATE="cloud"
	cd "$workdir" || die $EXIT_CLONE "cannot enter $workdir"

	# The two binaries, both required. ticfac refuses to start without the
	# executor beside it — correctly: a run without it would start jobs nothing
	# is watching — and the refusal is worth more here than at the first
	# dispatch, because this container has already paid for the clone and the
	# pre-flight by now.
	local missing=()
	command -v ticfac >/dev/null 2>&1 || missing+=(ticfac)
	command -v ticfac-exec-subprocess >/dev/null 2>&1 || missing+=(ticfac-exec-subprocess)
	if ((${#missing[@]} > 0)); then
		die $EXIT_CONFIG "this image does not carry ${missing[*]} — the orchestrator IS ticfac, so there is nothing for this container to run. Rebuild the image from a deploy that stages it (internal/factory/ticfacbin.go)."
	fi

	# The workers this run dispatches run in their OWN containers (tick gbs).
	# Every role resolves the CLOUD profile set — the --profiles flag below
	# points run-epic at the profiles-cloudflare-sandbox/ the image installs at
	# /usr/local/share/ticfac — whose executor is cloudflare-sandbox: one
	# worker container per attempt, booted by this run's factory over its
	# per-tick sandbox door, authenticated by TICKS_FACTORY_URL and
	# TICKS_FACTORY_TOKEN above. No worker shares this container. The
	# compiled-in profiles/ name executor local-subprocess, and dispatching
	# with them is how the 2026-09-23 smoke tick ran every worker as a
	# subprocess of the orchestrator's own container — the exact finding this
	# tick closes. A worker's model credential is issued in ITS boot by the
	# factory, from the run's own gateway token; this container hands the
	# workers nothing but the door.

	# Two requirements the claude worker used to impose on this boot are
	# deliberately NOT here any more (tick mdw).
	#
	# No route refusal. The boot used to die on any model not on the Anthropic
	# route, on the reasoning that the worker was the claude CLI, which speaks
	# the Anthropic API — a refusal that rejected exactly the workers-ai route
	# this block now wires. Whether a routed model can be called is answered
	# where the route is selected and probed (select_model_route, probe_model,
	# before this line ever runs), not by the worker CLI.
	#
	# No IS_SANDBOX export. That was the claude CLI's requirement: it refuses
	# ` + "`--permission-mode bypassPermissions`" + ` under root unless told it is in a
	# sandbox. pi has no equivalent check — it has no permission gate at all;
	# its only approval-shaped flag (` + "`--approve`" + `) is trust of project-local
	# files, verified live 2026-09-10 (tick gjk; herdr-kinds.md's pi section,
	# kinds.go's pi row) — so the requirement went with the CLI that had it.

	# The base branch, and the LOCAL ref that makes it resolvable.
	#
	# The clone above is a ` + "`git init`" + ` plus a fetch of one SHA, so the checkout
	# holds the submitted commit and nothing else: ` + "`git rev-parse main`" + ` fails,
	# and rev-parse does not fall back to refs/remotes/origin/main the way
	# ` + "`git checkout`" + ` DWIMs. The reconciler resolves --base locally when it
	# creates the integration branch, so a base that names a branch nothing
	# fetched is refused at the first leg — which is exactly what a container run
	# did before this line. Fetching it is what a local checkout already has.
	local base_branch
	base_branch="$(ticfac_base_branch)"
	if ! git -C "$workdir" fetch -q origin "+refs/heads/${base_branch}:refs/heads/${base_branch}"; then
		die $EXIT_CLONE "cannot fetch the base branch ${base_branch} from origin — ticfac cuts the epic's integration branch from it, and a base the checkout does not hold is refused before any tick is claimed"
	fi

	# Full history, because ticfac MERGES. The clone above is a depth-1 fetch
	# of one commit, which is all a harness ever needed; ticfac folds the
	# default branch into the integration branch on every refresh (tick wvd),
	# and a merge needs the common ancestor. In a depth-1 checkout there is
	# none, and git refuses: "refusing to merge unrelated histories" — the
	# first pi smoke run to reach the reconciler stopped on exactly that.
	if [[ "$(git -C "$workdir" rev-parse --is-shallow-repository 2>/dev/null)" == "true" ]]; then
		if ! git -C "$workdir" fetch -q --unshallow origin; then
			die $EXIT_CLONE "cannot fetch the repository's history from origin — ticfac merges the default branch into the epic's integration branch, and a merge in a depth-1 checkout has no common ancestor to find"
		fi
	fi

	# --branch is left at ticfac's default, epic/<epic-id>, and NOT pointed at
	# ${run_branch}: the reconciler requires its integration branch to be checked
	# out nowhere (internal/reconcile/git.go, worktreeAt), and adopt_run_branch
	# has this checkout sitting on ${run_branch}. Durability is not lost by that.
	# ticfac pushes the integration branch to origin as it goes — that is axiom
	# 1, and it is what a fresh orchestrator re-derives from — while the keeper
	# goes on doing what it does here: pushing whatever this checkout commits,
	# and printing the heartbeat that is the only view an operator has of a
	# container they cannot reach.
	# --base is the SUBMITTED COMMIT, not the default branch (ticfac tick rf3).
	# --base is where the integration branch is cut from, and the operator
	# submitted a commit: cutting from the default branch instead threw the
	# submission away, so an epic that exists only on the submitted branch was
	# "not found" and the Workflow re-booted into the same failure. What flows
	# IN afterwards is the remote's default branch, which the reconciler now
	# resolves for itself (ticfac tick wvd) — asking the remote, since this
	# checkout holds no origin/HEAD. The base branch is still fetched above so
	# it is a ref this checkout holds.
	local cmd=(
		ticfac run-epic
		--repo "$workdir"
		--remote origin
		--base "$base_sha"
		--run-id "$run_id"
		--profiles ` + cloudProfilesContainerPath + `
		"$epic"
	)

	# --profiles is what makes this a CLOUD run (tick gbs): the compiled-in
	# set is the LOCAL one — executor local-subprocess — and a run-epic that
	# resolved it would dispatch every worker of this epic as a subprocess of
	# this very container. The cloud set pairs every role with executor
	# cloudflare-sandbox, so each worker boots in its own container through
	# this run's factory, and the reconciler refuses at construction if the
	# image did not carry the set — fail closed, before any tick is claimed.

	# Started BEFORE the exec, watching this pid: exec keeps the pid, so the
	# keeper is watching ticfac itself and dies when it does.
	start_keeper "$$"
	say "starting ticfac run-epic ${epic} at ${base_sha:0:12}, refreshing from ${base_branch} — a deterministic reconciler, with no model deciding control flow"
	# exec, so ticfac owns stdout directly: its output streams as it is produced
	# and its exit status is the run's exit status.
	exec "${cmd[@]}"
}
` + ticfacEntrypointCloseMarker + "\n"
