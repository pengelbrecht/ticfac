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

// fileInfrastructure marks an attempt whose container died in its boot on a
// service outside it (ExitGatewayUnavailable, ExitOriginUnavailable): written
// by the Inspect that observed the settle, read by the collect, which has only
// an empty landing branch to go on otherwise (epic hn6, run_37b36bfe).
const fileInfrastructure = "infrastructure.json"

// infrastructureService names what a boot exit code says failed outside the
// tick, and whether a retry reaches the same answer, or "" for a code that is
// not an infrastructure failure. A tk the image does not pin (ExitTkVersion)
// is the container itself: no tier boots a different image, so it spends no
// rung either, and it is not retried.
func infrastructureService(code int) (string, bool) {
	switch code {
	case sandboximage.ExitGatewayUnavailable:
		return "the model gateway", false
	case sandboximage.ExitOriginUnavailable:
		return "origin", false
	case sandboximage.ExitTkVersion:
		return "the worker image's tk", true
	}
	return "", false
}

// infrastructureExit is the infrastructure exit code a terminal status says
// the container's work process exited with, or 0.
func infrastructureExit(status *subprocess.JobStatus) int {
	for _, code := range []int{sandboximage.ExitGatewayUnavailable, sandboximage.ExitOriginUnavailable,
		sandboximage.ExitTkVersion} {
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
