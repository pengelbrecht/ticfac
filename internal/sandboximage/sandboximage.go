// Package sandboximage is the Go spelling of the sandbox image's contract —
// the container a cloud run boots, whose build context is image/ at the
// repository root — and the home of the tests that run the image's scripts.
//
// ticfac authors image/ (tick r6w). Until then the tree was vendored from
// ticks' cloud/sandbox, and the suite that drives the scripts (ticks
// internal/sandbox) lived beside ticks' copy; it moved here with the tree, so
// the scripts that ship and the scripts that are tested are the same bytes by
// construction rather than by a pinned digest.
//
// Nothing here builds or runs the image. The constants below are what the
// scripts read and print — environment names, exit codes, markers — stated
// once so the tests and the scripts cannot drift silently. Three of them are
// also pinned by contracts/worker-boot-contract.json, which the control plane
// (cloudflare/src/worker-boot.ts) reads too.
//
// Layering, which the pins depend on: the image is built and pushed at deploy
// cadence (`ticfac factory deploy`) and then instantiated per run. Starting
// the Nth container is a cold start of an existing image, never a rebuild.
// Repository dependencies are therefore never baked in; they live in a cache
// directory the entrypoint points every toolchain at. A cold cache costs time,
// never correctness.
package sandboximage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pengelbrecht/ticfac"
)

// Env names the entrypoint's inputs. They are TICKS_-prefixed like the rest of
// the runner-facing environment, except the gateway base URL, which keeps the
// name the factory stores it under so one value has one spelling from
// `ticfac factory setup` to the container.
const (
	EnvRepoURL        = "TICKS_REPO_URL"
	EnvBaseSHA        = "TICKS_BASE_SHA"
	EnvEpic           = "TICKS_EPIC"
	EnvGatewayBaseURL = "AI_GATEWAY_BASE_URL"
	// EnvGatewayToken is the run's own gateway credential (D17), minted per
	// orchestrator boot by the Run Workflow. It is the ONLY model credential
	// the container holds: the operator's vendor key stays in the control
	// plane, which exchanges it per request, stamps the run and tick ids on
	// the request's gateway metadata, and can revoke this token mid-run.
	EnvGatewayToken = "AI_GATEWAY_TOKEN"
	EnvHarness      = "TICKS_HARNESS"
	// EnvModel is the model the harness runs on. The control plane may set it;
	// when it does not, the entrypoint fills it from the checkout's own
	// role/tier routing, and a boot with neither is refused rather than
	// started — a harness with no model hangs instead of failing.
	EnvModel = "TICKS_MODEL"
	// EnvModelProvider and EnvModelID are what the entrypoint DERIVED from the
	// routed model: which gateway route serves it, and the id in that
	// provider's own namespace (the `@cf/…` Workers AI id, the bare Anthropic
	// name). Exported so the harness's environment records the decision the
	// probe verified, rather than leaving it implicit in a base URL.
	EnvModelProvider = "TICKS_MODEL_PROVIDER"
	EnvModelID       = "TICKS_MODEL_ID"
	// EnvModelProbeTimeout bounds the pre-flight model probe, in seconds.
	EnvModelProbeTimeout = "TICKS_MODEL_PROBE_TIMEOUT"
	// EnvModelProbeTries and EnvModelProbeBackoff are how often a probe that
	// got no answer (or a transient 429/502/504) is asked again, and the
	// seconds × the try number waited in between.
	EnvModelProbeTries   = "TICKS_MODEL_PROBE_TRIES"
	EnvModelProbeBackoff = "TICKS_MODEL_PROBE_BACKOFF"
	// EnvHarnessProbeTimeout bounds the pre-flight HARNESS probe, in seconds.
	// It is a bigger number than the model probe's on purpose: this one starts
	// a whole agent CLI, not one curl.
	EnvHarnessProbeTimeout = "TICKS_HARNESS_PROBE_TIMEOUT"
	// EnvHarnessProbe marks the one harness invocation that is a pre-flight
	// round-trip rather than the run itself. The container starts the same
	// binary twice and only one of them is the orchestrator, so the difference
	// is stated in the environment rather than inferred from argv.
	EnvHarnessProbe = "TICKS_HARNESS_PROBE"
	// EnvSubstrate is the explicit dispatch-substrate override the container
	// runs under. It is the same spelling tk's reader uses
	// (ticks runnersconfig.SubstrateEnvVar) rather than a second one: the entrypoint
	// exports it, `ticfac sandbox substrate` resolves it, and the harness and
	// everything it spawns inherit it. A cloud sandbox has no herdr server, so
	// a repository whose tracked config pins herdr for its LOCAL runs is told
	// the effective substrate here instead of having its checkout rewritten.
	EnvSubstrate  = "TICKS_SUBSTRATE"
	EnvMaxTime    = "TICKS_MAX_TIME"
	EnvWorkdir    = "TICKS_WORKDIR"
	EnvCacheDir   = "TICKS_CACHE_DIR"
	EnvRunID      = "TICKS_RUN_ID"
	EnvTkVersion  = "TICKS_TK_VERSION"
	EnvPhase      = "TICKS_PHASE"
	EnvStopReason = "TICKS_STOP_REASON"
	// EnvSandboxImage is the image reference the control plane says it booted.
	// It is advisory and read-only from inside: the container cannot change
	// what it is running, so this exists so that a repository declaring a
	// DIFFERENT image in its `[sandbox]` table is reported in the boot log
	// rather than silently ignored.
	EnvSandboxImage = "TICKS_SANDBOX_IMAGE"
	// EnvKeeperInterval is how often the run keeper pushes the run branch and
	// prints a heartbeat, in seconds. 0 turns it off, which is a deliberate
	// choice a caller has to make: the default is on, because a run that
	// pushes nothing until closeout loses everything a killed container held.
	EnvKeeperInterval = "TICKS_KEEPER_INTERVAL"
	// EnvRunBranch is the branch the run's commits land on and the keeper
	// pushes. The entrypoint DERIVES it (see [RunBranch]) and exports it, so
	// the orchestrator and everything it spawns name the same branch the
	// control plane can recover work from.
	EnvRunBranch = "TICKS_RUN_BRANCH"
	// The RunRoom-backed operator bridge. The token is injected only into the
	// ephemeral sandbox and is never written into the checkout.
	EnvFactoryURL     = "TICKS_FACTORY_URL"
	EnvFactoryToken   = "TICKS_FACTORY_TOKEN"
	EnvFactoryProject = "TICKS_FACTORY_PROJECT"
	// EnvFactoryMaxInstances is the account's container ceiling — the
	// factory's FACTORY_MAX_INSTANCES mirror of `[[containers]] max_instances`
	// — handed to the ORCHESTRATOR container only, so the run it drives never
	// dispatches more worker containers than the account can run beside it
	// (hn6's cloud run: orchestrator + two workers filled a ceiling of three,
	// and the third worker's start waited for a slot until it timed out).
	EnvFactoryMaxInstances = "TICKS_FACTORY_MAX_INSTANCES"
)

// RunBranchPrefix is the namespace a run's own branch lives in. It is half of
// the branch-name ownership test the factory applies to a pull request (D9,
// with `tick/*` for worker branches), so a run branch is recognisable as the
// factory's work by its name alone.
const RunBranchPrefix = "tick-run/"

// RunBranch is the branch one run's commits land on, pushed continuously by
// the container's run keeper.
//
// The name is derived from the epic rather than the run, because a run is
// recovery state: a boot that finds this branch on origin CONTINUES it —
// whether it is a rebooted orchestrator or a new run for an epic whose
// previous run was killed — instead of starting the epic over. A branch that
// does not descend from the boot's base belongs to a run at another base and
// is left alone; the entrypoint pushes beside it rather than over it.
func RunBranch(epic string) string {
	return RunBranchPrefix + epic
}

// Phase is what a boot is for. The Run Workflow owns the run's lifecycle and
// can only reach the image through the environment, so "this is a reboot after
// the orchestrator died" and "this run is stopping cleanly" are variables
// rather than a channel the harness has to be listening on.
//
// The distinction is load-bearing twice over. A reboot must reconcile before
// it does anything else — the sandbox is expected to die, and the fresh one
// adopts pushed state instead of redoing merged work. And a stop must still
// reach review and closeout, because an abandoned run leaves merged work with
// no tracker state (D15, UC1b). Neither decision is ever the agent's: budget
// and stop enforcement live in the Workflow, never in a prompt.
const (
	PhaseRun       = "run"       // first boot of a run: work the epic
	PhaseReconcile = "reconcile" // a fresh orchestrator after one died
	PhaseWave      = "wave"      // between container waves: integrate, then dispatch the next
	PhaseCloseout  = "closeout"  // a clean stop: no new work, review and close
	// PhaseReview is a pull request review (UC5, tick v7g): read one pull
	// request's diff, write findings, hand them to the factory, exit. It is
	// the one phase that is not about an epic — nothing is claimed, nothing is
	// committed and nothing is pushed, because a review run is issued the
	// read-only credential grade and could not push if its prompt told it to.
	PhaseReview = "review"
)

// Phases lists the accepted values of EnvPhase.
var Phases = []string{PhaseRun, PhaseReconcile, PhaseWave, PhaseCloseout, PhaseReview}

// EnvPass is which container wave this boot may ask for (tick wiy).
//
// Set only on a [PhaseWave] boot, and the in-run dispatch endpoint refuses a
// request that carries no pass number — so "may this container dispatch a
// wave" is a fact about how the control plane booted it, not a judgement the
// agent inside it makes. A closeout has no pass, and therefore no way to start
// new work even if its prompt were talked around.
const EnvPass = "TICKS_PASS"

// EnvWaveTicks and EnvWaveBase are the wave a [PhaseWave] boot INHERITS: the
// ticks the control plane just dispatched, comma-separated, and the commit
// their containers cloned at.
//
// They exist because every pass of a cloud run is a fresh container, and the
// manifests `tk cloud spawn` writes live under `.tick/logs/`, which is
// git-ignored local state. The pass that must fan a wave back in is therefore
// never the container that dispatched it, and would otherwise be told "no
// cloud dispatch is recorded — was this wave spawned from another checkout?"
// about a wave its own run had just run. The control plane is the one party
// that certainly knows, so it says so (tick wiy).
const (
	EnvWaveTicks = "TICKS_WAVE_TICKS"
	EnvWaveBase  = "TICKS_WAVE_BASE"
)

// EnvReviewPR and EnvReviewHeadSHA are the pull request a [PhaseReview] boot
// reviews, and the commit it reviews (tick v7g).
//
// They exist for [EnvWaveTicks]'s reason, sharpened: the container is TOLD
// which pull request it is looking at, because the binding between a run and a
// pull request lives in the control plane's own record and nowhere the
// container could reach. A container that named its own pull request would be
// a container that could comment on somebody else's — and the factory's review
// door takes the number from that record too, so a container that lied here
// would only be lying to itself.
//
// EnvReviewOutput is where the harness writes its findings: the entrypoint
// reads that file and POSTs it, rather than the agent making the call, so what
// leaves the container is one bounded body to one endpoint.
const (
	EnvReviewPR      = "TICKS_REVIEW_PR"
	EnvReviewHeadSHA = "TICKS_REVIEW_HEAD_SHA"
	EnvReviewOutput  = "TICKS_REVIEW_OUTPUT"
)

// ReviewFindingsPath is where a [PhaseReview] container's harness writes its
// findings, derived from the run id so two containers cannot collide.
func ReviewFindingsPath(runID string) string {
	return "/tmp/ticks-review-" + runID + ".md"
}

// ReviewRef is the ref a review container fetches: GitHub serves a pull
// request's head from the BASE repository, so a fork's branch is readable
// through the same remote — and therefore through the factory's read-only git
// door, with no second credential and no second door.
func ReviewRef(pr string) string {
	return "refs/pull/" + pr + "/head"
}

// Actor is what the entrypoint exports as TK_ACTOR, joining the runner-shaped
// actor namespace so the verdict guard's human-attestation rule applies to
// cloud runs with no new code.
const Actor = "cloud:orchestrator"

// Exit codes the entrypoint uses before the harness takes over. Distinct
// classes stay distinct: a missing gateway and a red pre-flight are different
// operator problems and must never share a code or a message.
const (
	ExitConfig    = 2 // a required input is missing, malformed, or points at a vendor
	ExitClone     = 3 // clone/checkout of the submitted SHA failed
	ExitTkVersion = 4 // tk is absent or is not the version the image pins
	ExitPreflight = 5 // an [environment.commands] pre-flight check failed
	// ExitSetup reports that the repository's own `[sandbox]` declaration was
	// not satisfied: a setup command failed, or the container is not the
	// `[sandbox].image` the checkout declares (tick x3v). It is deliberately
	// not best effort like toolchain provisioning: a repository that declares
	// a warm step, or an image, and does not get it starts a wave in which
	// every worker fails the same way, at model prices.
	ExitSetup = 6
	// ExitModel reports that the container has a gateway and no model it can
	// actually call: nothing routed, a model whose provider cannot be named, a
	// model the chosen harness does not speak, or a gateway that refused a
	// one-token probe. It is its own class because the alternative is the
	// worst failure this image can produce — a harness that starts cleanly,
	// reaches the skill loop and then hangs forever on its first model call,
	// which is exactly the green-start trap with no error to read.
	ExitModel = 7
	// ExitHarness reports the gap between "the gateway answers" and "the
	// harness can call it". They are different problems with different fixes:
	// a green model probe followed by a harness that dies at start means the
	// route is fine and the harness's own provider wiring is not — which is
	// how a run reached the skill loop and died with "No API key found for
	// cloudflare-ai-gateway" while the probe had just passed. Collapsing the
	// two into ExitModel would send an operator to look at the gateway.
	ExitHarness = 8
	// ExitReview reports a review whose findings never reached the factory
	// (tick v7g). Its own class because it is the one failure where the run
	// did all of the work and kept none of it: the diff was read and the model
	// was paid for, and the comment — the only durable thing a read-only run
	// produces — does not exist. It is deliberately NOT terminal: a post can
	// fail for a reason a second attempt survives, and the review door answers
	// a boot whose earlier attempt DID land with a refusal the container reads
	// as success.
	ExitReview = 12
	// ExitStartUnpublished reports a start commit origin does not serve: the
	// fetch worked, and the SHA the job was dispatched on is not among what it
	// brought. It is the DISPATCHER's failure, not the container's — a job cut
	// at a commit only the orchestrator's own clone holds (epic hn6,
	// run_09ebaf29: a base fold's conflicted merge, and three resolve jobs
	// lost to it as missing-result) — and it is distinct from ExitClone
	// because it is the one checkout failure a retry as-is cannot survive.
	ExitStartUnpublished = 13
	// ExitGatewayUnavailable reports a gateway that gave no usable answer to
	// the one-token probe through the boot's whole retry window: no HTTP
	// answer, or a timeout, rate limit or bad gateway from it or its upstream.
	// ExitOriginUnavailable is the same for origin's fetch. They are
	// INFRASTRUCTURE, never a verdict on the tick: the boot never reached the
	// harness, and the orchestrator dispatches the job again at the same tier
	// rather than spending a rung of the ladder (epic hn6, run_37b36bfe: 0rx's
	// worker probed the gateway while the factory Worker was being
	// redeployed, gave up after one 30s try with exit 7, and the run escalated
	// 0rx to the ceiling over it). Distinct from ExitModel and ExitClone,
	// which stay the verdicts a retry as-is reaches again: a refusal the
	// gateway answered, a fetch the remote refused.
	ExitGatewayUnavailable = 14
	ExitOriginUnavailable  = 15
)

// Script names the files the image installs.
//
// One image plays two roles (tick x3v): entrypoint.sh is the orchestrator's
// run entrypoint and worker.sh is a per-tick worker's, and CommonScript is the
// role-neutral half both source — the gateway and model wiring, the harness
// probe, caches, the clone, provisioning, setup and the pre-flight. It is a
// library rather than a third entrypoint, and it is sourced rather than copied
// so a fix to any of that cannot land in one role and miss the other.
const (
	EntrypointScript = "entrypoint.sh"
	WorkerScript     = "worker.sh"
	CommonScript     = "common.sh"
	PreflightScript  = "preflight.sh"
	DockerfileName   = "Dockerfile"
)

// Dir returns the absolute path of image/, the image's build context, found by
// walking up from the working directory to the module root.
func Dir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "image"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", wd)
		}
		dir = parent
	}
}

// Path returns the absolute path of one file in the sandbox asset directory.
func Path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// ImageName is the image this repository builds from image/. It carries
// no registry: the operator pushes it into their own account, and the run path
// prefixes whatever registry that is.
const ImageName = "ticks-orchestrator"

// PinnedTkVersion reports the tk version the image embeds, read from the
// Dockerfile so the pin is stated once. The Dockerfile is read from the
// EMBEDDED image context, never off the working tree: the one caller that
// needs the default (`ticfac sandbox image`) also runs inside worker
// containers, where no ticfac checkout sits above the cwd for [Dir]'s walk
// to find — the tree walk made the verb exit 1 there, which image/common.sh
// swallowed, and the container's declared-image check never refused a
// mismatched image (tick bib). The embedded copy is also the honest source
// for a shipped binary: the pin and the code that reads it are one commit by
// construction, and TestTheImageTreeIsWhatTheBinaryShips keeps it equal to
// the tree.
func PinnedTkVersion() (string, error) {
	b, err := fs.ReadFile(ticfac.SandboxFS(), "image/"+DockerfileName)
	if err != nil {
		return "", err
	}
	m := tkVersionArg.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("%s declares no ARG TK_VERSION", DockerfileName)
	}
	return string(m[1]), nil
}

var tkVersionArg = regexp.MustCompile(`(?m)^ARG\s+TK_VERSION=(\S+)`)

// DefaultImage is the image reference a run boots when nothing else is asked
// for. It is a default, not a constant: whatever starts a sandbox must take the
// reference as a parameter, so a project that pins its own image is an
// argument at the call site rather than a change to the call site.
func DefaultImage() (string, error) {
	version, err := PinnedTkVersion()
	if err != nil {
		return "", err
	}
	return ImageName + ":" + version, nil
}

// This file is the Go half of the per-tick WORKER container's contract —
// image/worker.sh, the entrypoint a worker sandbox runs (tick tap).
//
// It exists because the contract has three readers and they must not drift.
// The script is one. `cloudflare/src/worker-boot.ts` is another: the
// dispatcher (`worker-dispatch.ts`, tick 0ds) needs the probe command, the
// string the probe's output must contain and the environment the real command
// gets, and it must be TOLD them rather than guessing — the module says so in
// as many words. This package is the third, and it is what the repository's
// tests check the other two against, through the shared fixture the parity
// test reads.
//
// One image, two roles. On Cloudflare an image belongs to the containers
// application rather than to a boot (tick x3v), so the worker container is the
// SAME image as the orchestrator container. What tells it which role it is
// playing is which entrypoint the control plane starts inside it: an
// orchestrator boot runs [OrchestratorCommand], a worker boot runs
// [WorkerCommand]. There is deliberately no role variable beside that — a
// container whose role is a flag can be started in the wrong one, while a
// container whose role is the command it was given cannot.

// The two run entrypoints the image installs. The Cloudflare sandbox control
// server keeps the container ENTRYPOINT; these are what a boot starts inside
// the running sandbox.
const (
	OrchestratorCommand = "/usr/local/bin/ticks-orchestrator"
	WorkerCommand       = "/usr/local/bin/ticks-worker"
)

// WorkerProbeArg turns [WorkerCommand] into the green-start probe.
const WorkerProbeArg = "--probe"

// WorkerProbeCommand is the trivial work a worker sandbox is asked to do
// BEFORE any real work is dispatched to it, per the green-start trap the cloud
// design requires implemented rather than assumed.
func WorkerProbeCommand() string { return WorkerCommand + " " + WorkerProbeArg }

// WorkerProbeMarker is the string the probe's stdout must CONTAIN for the
// sandbox to count as launched.
//
// Content, never the exit status: a probe that exits 0 having printed the
// wrong thing is precisely the trap (`.tick/learnings.md` — an `npx` version
// probe that prints npm's own version exits 0). It carries no version, date or
// id, because both halves of the check are a substring match and anything
// varying would make them drift.
const WorkerProbeMarker = "ticks-worker-probe-ok"

// The worker entrypoint's own inputs, beside the ones every role takes
// (EnvRepoURL, EnvBaseSHA, EnvEpic, the gateway pair, EnvHarness, …).
const (
	// EnvTick is the one tick this container implements. A worker sandbox
	// without it has nothing to do and refuses to boot rather than running
	// something else's prompt.
	EnvTick = "TICKS_TICK"
	// EnvWorkerBranch is the branch the worker's commits and its report land
	// on, exported for everything the harness spawns. The entrypoint DERIVES
	// it (see [WorkerBranch]); nothing supplies a second spelling.
	EnvWorkerBranch = "TICKS_WORKER_BRANCH"
	// EnvWorkerTimeout bounds the harness, in seconds; 0 leaves it unbounded.
	//
	// It exists because the dispatcher's own wait timeout ends in the
	// container being KILLED, and a killed container pushes nothing. A worker
	// bounded just under the dispatcher's bound turns a hung agent into a
	// pushed branch and a report — the difference between a lost tick and a
	// legible one.
	EnvWorkerTimeout = "TICKS_WORKER_TIMEOUT"
	// EnvWorkerSetup is whether this worker runs the repository's own
	// `[sandbox]` setup: `always` (the default) or `skip`.
	//
	// It is a switch because the measurement says it is the one that matters:
	// fan-out per-sandbox time degrades 3.74x at N=5 and tick kuf found ALL of
	// that in dependency install rather than the image pull. `always` is
	// correct — a worker that cannot run the repository's tests cannot
	// implement a tick — and a wave whose ticks touch no dependencies can opt
	// out of paying it N times.
	EnvWorkerSetup = "TICKS_WORKER_SETUP"
	// EnvWorkBaseSHA is, for a CARRIED attempt, the base the carried work was
	// cut from (epic hn6, run_3f034e68). A carried attempt boots AT the
	// released attempt's head, so its own commits are counted from there; a
	// worker that finds the carried work complete and adds nothing is still
	// delivering work, and this is what lets the container see it and settle
	// the attempt succeeded. Absent for every attempt that carries nothing.
	EnvWorkBaseSHA = "TICKS_WORK_BASE_SHA"
	// EnvTraceID is the identifier that joins the message which produced this
	// tick to the container now working on it (D20, tick hyi).
	//
	// It is passed IN, never minted here: the id was minted at whichever edge
	// the work entered the factory through — an ingested signal or a run
	// submission — and a container that minted its own would name a chain
	// nothing upstream shares. Absent for work that entered through no traced
	// edge, which the entrypoint treats as "say nothing" rather than
	// inventing a value.
	//
	// The container prints it so a human reading the log sees it; the control
	// plane also writes it as a banner at the head of the container's R2
	// stream, because a container that crashes before it prints anything is
	// exactly the one being read.
	EnvTraceID = "TICKS_TRACE_ID"
	// EnvRolePrompt is the rendered role prompt a dispatch carries into the
	// container (tick nue; ticfac yoh tick 9iz): the profile's own prompt
	// TEXT: UTF-8 prose with no control character but tab, LF and CR, at
	// most 65536 bytes (the door's rule since ticfac #66), the same
	// text the run's records digest into `prompt_digest`.
	//
	// When it is set the worker runs its harness on it verbatim; when it is
	// absent (a factory that predates it, or the image driven by hand) the
	// worker renders its prompt from the checkout with `ticfac sandbox
	// worker-prompt`, as it always has.
	EnvRolePrompt = "TICKS_ROLE_PROMPT"
)

// Values of [EnvWorkerSetup].
const (
	WorkerSetupAlways = "always"
	WorkerSetupSkip   = "skip"
)

// WorkerActor is what the worker entrypoint exports as TK_ACTOR, joining the
// runner-shaped actor namespace beside [Actor].
const WorkerActor = "cloud:worker"

// The cancellation door (tick 7zk).
//
// The supervisor could say two things to a worker container — kill the process,
// destroy the container — and both of them mean "this container's work is
// gone". Run run_f7bd5a36 paid $8.00 for three containers that were all still
// working when the cost budget tripped and kept nothing: no branch, no report,
// no salvage. The salvage itself already existed (tick 5fg) and runs when the
// WORKER's own bound fires; nothing ran it when the SUPERVISOR ended the wave.
//
// [WorkerCancelCommand] is the missing third thing to say: STOP AND PUSH NOW.
// It is a second process started inside the same container, which lodges the
// request and asks the harness — never the entrypoint — to stop, so the
// entrypoint returns from its harness call exactly as it does at its own bound
// and every line after it (sweep, salvage, report, push) runs.
//
// It is safe to hold a window open for because of the ORDER around it: the
// run's gateway credential is revoked before the ask (tick gyl), so a
// container in the window cannot make a model call at all — it can only finish
// a git push. Money dies first, work is rescued second.
const (
	// WorkerCancelArg turns [WorkerCommand] into the cancellation door. It
	// takes one optional argument, the machine-readable reason a wave was
	// cancelled (`budget:cost`, `stopped:hard`, …).
	WorkerCancelArg = "--cancel"

	// WorkerCancelMarker is what the cancellation door prints once the request
	// is lodged, so the dispatcher can tell a container that took the ask from
	// one that could not. Content, not an exit code — the same rule the
	// green-start probe's marker exists for.
	WorkerCancelMarker = "ticks-worker-cancel-requested"

	// WorkerCancelReportMarker heads the section the entrypoint prepends to
	// `RESULT-<tick>.md` when the supervisor stopped the container. A reader of
	// the branch has to be able to tell a tick its agent abandoned from a tick
	// the RUN cut short: the two call for opposite next actions.
	WorkerCancelReportMarker = "CANCELLED BY THE SUPERVISOR"

	// EnvWorkerStateDir is where the container keeps the two facts a second
	// process inside it has to find — the harness's pid, and whether a
	// cancellation has been lodged. It has a fixed default because the two
	// processes share nothing else; it is overridable only so the repository's
	// tests can drive several workers at once.
	EnvWorkerStateDir = "TICKS_WORKER_STATE_DIR"
)

// WorkerCancelCommand is what the dispatcher starts inside a container it is
// about to destroy, to ask it to stop and push what it has.
func WorkerCancelCommand() string { return WorkerCommand + " " + WorkerCancelArg }

// The boundary guard (tick dxk).
//
// A worker agent may not run `tk` and may not write under `.tick/`: the
// orchestrator owns all tick state, and several workers of one wave each
// closing their own tick produce conflicting writes to the same
// `activity.jsonl` and issue files on branches that all merge into one
// integration commit. That is the conflict class the invariant exists to
// prevent, and D4's one-writer rule with it.
//
// It was a prose instruction until a real container ignored it — the worker
// prompt forbids it in the second line of its Boundaries section, and
// run_215b7cbff9dd405c80d738be45cccde5's tick 5jo ran `tk close` and committed
// the result anyway. The container is ours end to end, so the boundary is now
// ENFORCED there: the harness gets a PATH whose `tk` refuses, the clone gets a
// hook that refuses a commit staging tracker state, and the container's own
// salvage sweep leaves `.tick/` behind. None of it needs the agent's
// cooperation, which is the point.
//
// The strings below are the guard's contract with its readers. The refusal is
// what the agent is handed (and what the repository's tests assert it was);
// the report marker is what carries the attempt to a HUMAN, because a
// violation that is silently prevented trains nobody.
const (
	// WorkerTkDeniedMessage is what the harness's `tk` prints before refusing.
	WorkerTkDeniedMessage = "tk is not available to a worker agent"

	// WorkerBoundaryReportMarker heads the section the worker entrypoint
	// prepends to `RESULT-<tick>.md` when the agent tried to cross the
	// boundary. It is the string a reader — and `worker-collect.ts` — looks
	// for, so it carries no tick id, path or count.
	WorkerBoundaryReportMarker = "BOUNDARY VIOLATION ATTEMPTED"
)

// WorkerBranchPrefix is the namespace a per-tick worker's branch lives in. It
// is the other half of the branch-name ownership test the factory applies to a
// pull request (D9, with `tick-run/*` for run branches).
const WorkerBranchPrefix = "tick/"

// WorkerBranch is the branch one tick's worker container pushes: the name
// `worker-collect.ts` compares against the epic base and reads the report out
// of. Derived here and in the entrypoint from the same rule, so the container
// and the collector cannot disagree about where the work is.
func WorkerBranch(epic, tick string) string {
	return WorkerBranchPrefix + epic + "/" + tick
}

// WorkerResultFile is the report a worker's branch must carry. It mirrors
// internal/workerprompt.ResultFile: the filename carries the tick id because
// every worker of a wave branches from the same commit, and a shared name is
// an add/add conflict on the second merge.
func WorkerResultFile(tick string) string { return "RESULT-" + tick + ".md" }

// Exit codes the worker entrypoint adds to the shared 2-8. Each is a different
// thing to do about it, which is why they are not one code.
const (
	// ExitWorkerPush reports commits that exist and an origin that would not
	// take them: the work lives only in a container about to be destroyed.
	ExitWorkerPush = 9
	// ExitWorkerNoWork reports a branch and a report that reached origin with
	// no work commits on them — the harness exited 0 having done nothing,
	// which is the exit counterpart of the green-start trap (D23) and the
	// reason completion is proved from the durable layer rather than inferred
	// from a status.
	ExitWorkerNoWork = 10
	// ExitWorkerAgent reports a harness that failed, ran out of time, or
	// exited without writing its report. All three are the agent not
	// fulfilling its contract, and the report is half of it: a tick whose only
	// account of itself was written by the entrypoint is not a tick that
	// reported. Whatever it had committed was pushed first — this code
	// describes the agent, never the durability.
	ExitWorkerAgent = 11
)

// WorkerNudgeMax is how many times the worker entrypoint re-prompts a harness
// that ends its turn — exits 0 — without writing its report, before the
// container gives up and reports the tick itself (tick 060). The same bound
// as the local subprocess executor's (internal/exec/subprocess MaxNudges):
// a cloud worker and a local one give a stalling harness the same number of
// chances before the missing-result verdict.
const WorkerNudgeMax = 2

// WorkerPromptAddendum is what `ticfac sandbox worker-prompt` appends to the
// shared worker template.
//
// The template is written for a herdr worker in a worktree beside its
// orchestrator. Three things are different in a container and all three change
// what the agent must do: the checkout is a clone on the branch already, the
// container is destroyed at exit so only what is pushed survives, and the
// entrypoint — not the agent — commits the report and pushes the branch. An
// agent that pushed on its own, or that waited to be collected from a
// worktree, would be wrong in ways the template cannot warn it about.
func WorkerPromptAddendum(tick, branch string) string {
	return fmt.Sprintf(`
## Where you are running

You are in an ephemeral cloud container, not a worktree beside an orchestrator.
This checkout is a fresh clone and is already on %s. Everything you do here dies
with the container except what is committed on that branch — so commit as you
go, and do not wait until the end.

Do NOT push, and do not create or switch branches. When you exit, this
container's entrypoint commits %s and pushes %s for you; that push
is the only channel out. Terminal output is not read by anything.
`, branch, WorkerResultFile(tick), branch)
}

// DefaultCloudSubstrate is the substrate a cloud sandbox resolves when the
// control plane says nothing: harness dispatch. A container has no herdr
// server, so this is a deliberate choice, not a degradation, and the
// entrypoint announces it either way.
const DefaultCloudSubstrate = "harness"
