package cloudflaresandbox

import (
	"regexp"
	"strconv"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A settled worker's exit code, NAMED (epic hn6's second cloud run). r5i try 2
// settled as failed about five minutes after its checkout, with no model call
// and nothing pushed, and the run said only "settled as failed": the door's
// observation carried "the container's work process exited N", and N was the
// one fact that said which step of the container's boot died — the checkout's
// branch, tk, the model route, the harness, the repository's setup, the
// pre-flight. The worker's log is the other half (tick 86y); this half costs
// one line and needs no log at all. The classes are the image's own
// (internal/sandboximage), so the sentence is the entrypoint's vocabulary,
// never a second copy of it.

var exitedPattern = regexp.MustCompile(`exited (\d+)$`)

// exitClass is what one worker exit code means, or "" for a code the image
// does not assign.
func exitClass(code int) string {
	switch code {
	case sandboximage.ExitConfig:
		return "a required input was missing or malformed"
	case sandboximage.ExitClone:
		return "the checkout of the base or its worker branch failed"
	case sandboximage.ExitTkVersion:
		return "tk is absent or not the version the image pins"
	case sandboximage.ExitPreflight:
		return "an [environment.commands] pre-flight check failed"
	case sandboximage.ExitSetup:
		return "the repository's [sandbox] setup failed"
	case sandboximage.ExitModel:
		return "the container could not call its model (the gateway probe failed)"
	case sandboximage.ExitHarness:
		return "the harness could not use the model route (the harness probe failed)"
	case sandboximage.ExitStartUnpublished:
		return "the start commit is not on origin: the job was dispatched on a commit only its dispatcher's clone holds"
	case sandboximage.ExitGatewayUnavailable:
		return "the model gateway did not answer through the boot's retry window (infrastructure, not the tick)"
	case sandboximage.ExitOriginUnavailable:
		return "origin did not answer the fetch through the boot's retry window (infrastructure, not the tick)"
	case sandboximage.ExitWorkerPush:
		return "commits exist and origin would not take them"
	case sandboximage.ExitWorkerNoWork:
		return "the branch and report reached origin with no work commits"
	case sandboximage.ExitWorkerAgent:
		return "the harness failed, ran out of time, or left no report"
	case 124:
		return "a bounded step timed out"
	case 137:
		return "the process was killed (SIGKILL: out of memory, or the container was stopped)"
	case 143:
		return "the process was terminated (SIGTERM)"
	}
	return ""
}

// exitedWith reports whether a terminal status says the container's work
// process exited with `code`.
func exitedWith(status *subprocess.JobStatus, code int) bool {
	if status == nil || !status.Terminal {
		return false
	}
	for _, o := range status.Observations {
		if m := exitedPattern.FindStringSubmatch(o.Detail); m != nil && m[1] == strconv.Itoa(code) {
			return true
		}
	}
	return false
}

// fileStartUnpublished marks an attempt whose container could not check out
// its start commit because origin does not serve it (ExitStartUnpublished):
// written by the Inspect that observed the settle, read by the collect, which
// otherwise has only an empty landing branch to go on — and an empty branch
// reads as a job that answered nothing.
const fileStartUnpublished = "start-unpublished.json"

// fileInfrastructure marks an attempt whose container stopped in its boot
// (bootFault): written by the Inspect that observed the settle with the exit
// code, read by the collect, which has only an empty landing branch to go on
// otherwise (epic hn6, run_37b36bfe). The worker's own boot-stopped marker on
// origin (#176) is the collect's second source for the same code, for an
// orchestrator that never saw the settle.
const fileInfrastructure = "infrastructure.json"

// bootFault is what a worker's boot exit code says stopped it before its
// harness started, or nil for a code that is not a boot stop. None of them is
// a verdict on the TICK — the harness never ran — so none earns the tier
// ladder a rung:
//
//   - the gateway and origin not answering (14, 15) are TRANSIENT: the job is
//     dispatched again at the same tier, bounded (epic hn6, run_37b36bfe);
//   - the rest are DETERMINISTIC environment faults — the inputs the factory
//     sent, the image's tk, the repository's pre-flight or setup, a model
//     route the gateway refused, a harness that cannot use the route. A retry
//     as-is reaches the same answer and a higher tier boots the same image
//     with the same repository, so the run stops at once, naming the cause
//     and what to fix (Persistent).
func bootFault(code int) *subprocess.InfrastructureFailure {
	f := func(service, fix string, persistent bool) *subprocess.InfrastructureFailure {
		return &subprocess.InfrastructureFailure{Service: service, ExitCode: code, Persistent: persistent, Fix: fix}
	}
	switch code {
	case sandboximage.ExitGatewayUnavailable:
		return f("the model gateway", "check that the factory is deployed and its model gateway answers "+
			"(`ticfac factory status`, and the factory's deploy workflow), then run the epic again", false)
	case sandboximage.ExitOriginUnavailable:
		return f("origin", "check that origin is reachable from the factory's containers, then run the epic again", false)
	case sandboximage.ExitConfig:
		return f("the worker's boot inputs", "the factory dispatched a worker with a required input missing or "+
			"malformed (the reason names it) — check the factory is deployed at this ticfac's version "+
			"(`ticfac factory status`), then run the epic again", true)
	case sandboximage.ExitTkVersion:
		return f("the worker image's tk", "the factory's worker image carries a tk other than the one it pins — "+
			"deploy a factory whose image is consistent (`ticfac factory status`), then run the epic again", true)
	case sandboximage.ExitPreflight:
		return f("the repository's environment pre-flight", "fix the failing check the reason names, or correct "+
			"it in .tick/runners.toml, then run the epic again", true)
	case sandboximage.ExitSetup:
		return f("the repository's [sandbox] setup", "fix the failing setup command the reason names in "+
			".tick/runners.toml (it must be idempotent), or the declared [sandbox].image, then run the epic again", true)
	case sandboximage.ExitModel:
		return f("the model route", "the gateway refused the routed model (the reason quotes it): configure the "+
			"provider behind the gateway with `ticfac factory setup`, or route the role at a model that provider "+
			"serves in .tick/runners.toml, then run the epic again", true)
	case sandboximage.ExitHarness:
		return f("the harness's model wiring", "the gateway answered and the harness could not use it (the reason "+
			"quotes it): the harness's provider wiring in the worker image (image/common.sh's kind table) is what "+
			"fixes it — deploy a factory with it fixed, then run the epic again", true)
	}
	return nil
}

// bootExit is the boot-stop exit code a terminal status says the container's
// work process exited with, or 0.
func bootExit(status *subprocess.JobStatus) int {
	for _, code := range []int{sandboximage.ExitGatewayUnavailable, sandboximage.ExitOriginUnavailable,
		sandboximage.ExitConfig, sandboximage.ExitTkVersion, sandboximage.ExitPreflight, sandboximage.ExitSetup,
		sandboximage.ExitModel, sandboximage.ExitHarness} {
		if exitedWith(status, code) {
			return code
		}
	}
	return 0
}

// nameExitClasses appends the class to every "exited N" observation the door
// answered with, so the settled line a person reads says which step died.
func nameExitClasses(status *subprocess.JobStatus) {
	if status == nil {
		return
	}
	for i, o := range status.Observations {
		m := exitedPattern.FindStringSubmatch(o.Detail)
		if m == nil {
			continue
		}
		code, err := strconv.Atoi(m[1])
		if err != nil || code == 0 {
			continue
		}
		if class := exitClass(code); class != "" {
			status.Observations[i].Detail = o.Detail + " (" + class + ")"
		}
	}
}
