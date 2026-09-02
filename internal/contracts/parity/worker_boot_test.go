package parity

import (
	"strings"
	"testing"
)

// contracts/worker-boot-contract.json — STRUCTURAL.
//
// The per-tick worker boot: the commands, the probe and cancel markers, the
// branch and RESULT naming, the environment variables and the exit codes. It
// is ticks' side of a handshake ticfac will be on the other end of — the Herdr
// executor (SPEC §12 Phase 2) boots exactly these commands and reads exactly
// these markers.
//
// Structural, not executable, and the reason is worth stating: executing it
// means booting a worker, and there is no worker here. What CAN be asserted
// without one is that the handshake is internally consistent and unambiguous —
// which is most of what a marker contract is. A marker that is a prefix of
// another marker, an exit code shared by two outcomes, or a branch example
// that does not follow its own prefix are all failures a consumer discovers at
// 3 a.m. and this reader discovers at build time.

const workerBootFile = "worker-boot-contract.json"

type workerBoot struct {
	OrchestratorCommand string `json:"orchestrator_command"`
	WorkerCommand       string `json:"worker_command"`
	ProbeArg            string `json:"probe_arg"`
	ProbeCommand        string `json:"probe_command"`
	ProbeMarker         string `json:"probe_marker"`
	CancelArg           string `json:"cancel_arg"`
	CancelCommand       string `json:"cancel_command"`
	CancelMarker        string `json:"cancel_marker"`
	CancelReportMarker  string `json:"cancel_report_marker"`
	WorkerActor         string `json:"worker_actor"`
	BranchPrefix        string `json:"branch_prefix"`
	BranchExample       struct {
		Epic   string `json:"epic"`
		Tick   string `json:"tick"`
		Branch string `json:"branch"`
	} `json:"branch_example"`
	ResultFileExample struct {
		Tick string `json:"tick"`
		Path string `json:"path"`
	} `json:"result_file_example"`
	Env   map[string]string `json:"env"`
	Trace struct {
		BannerMarker  string `json:"banner_marker"`
		BannerExample string `json:"banner_example"`
	} `json:"trace"`
	SetupModes map[string]string `json:"setup_modes"`
	Boundary   struct {
		TkDenied     string `json:"tk_denied"`
		ReportMarker string `json:"report_marker"`
	} `json:"boundary"`
	ExitCodes map[string]int `json:"exit_codes"`
}

// The commands are built from their parts, so a probe command that stopped
// being "the worker command plus the probe arg" is caught rather than assumed.
func TestWorkerCommandsAreBuiltFromTheirParts(t *testing.T) {
	var c workerBoot
	readContract(t, workerBootFile, &c)

	if c.WorkerCommand == "" || c.OrchestratorCommand == "" {
		t.Fatal("the contract names no worker or orchestrator command")
	}
	if c.WorkerCommand == c.OrchestratorCommand {
		t.Error("the worker and the orchestrator are the same command; they are booted with different credentials")
	}
	if got := c.WorkerCommand + " " + c.ProbeArg; got != c.ProbeCommand {
		t.Errorf("probe_command is %q; worker_command + probe_arg is %q", c.ProbeCommand, got)
	}
	if got := c.WorkerCommand + " " + c.CancelArg; got != c.CancelCommand {
		t.Errorf("cancel_command is %q; worker_command + cancel_arg is %q", c.CancelCommand, got)
	}
	if !strings.HasPrefix(c.ProbeArg, "--") || !strings.HasPrefix(c.CancelArg, "--") {
		t.Errorf("the probe and cancel args must be flags: %q %q", c.ProbeArg, c.CancelArg)
	}
}

// Markers are matched in output, so no marker may be a prefix or substring of
// another: an executor scanning for "cancel requested" must not find it inside
// the line that means something else.
func TestNoMarkerIsASubstringOfAnother(t *testing.T) {
	var c workerBoot
	readContract(t, workerBootFile, &c)

	markers := map[string]string{
		"probe_marker":         c.ProbeMarker,
		"cancel_marker":        c.CancelMarker,
		"cancel_report_marker": c.CancelReportMarker,
		"trace banner_marker":  c.Trace.BannerMarker,
		"boundary report":      c.Boundary.ReportMarker,
	}
	for name, value := range markers {
		if value == "" {
			t.Errorf("%s is empty; an empty marker matches every line", name)
		}
	}
	for nameA, a := range markers {
		for nameB, b := range markers {
			if nameA == nameB || a == "" || b == "" {
				continue
			}
			if strings.Contains(a, b) {
				t.Errorf("%s (%q) contains %s (%q); one match would mean two things", nameA, a, nameB, b)
			}
		}
	}
	if !strings.HasPrefix(c.Trace.BannerExample, c.Trace.BannerMarker) {
		t.Errorf("the trace banner example %q does not start with the marker %q",
			c.Trace.BannerExample, c.Trace.BannerMarker)
	}
}

// The branch a worker writes to and the report it leaves are the two things a
// collect looks for. Both are named by example, and both examples are checked
// against the rule they illustrate.
func TestBranchAndResultNamingFollowTheirOwnRules(t *testing.T) {
	var c workerBoot
	readContract(t, workerBootFile, &c)

	if c.BranchPrefix == "" || !strings.HasSuffix(c.BranchPrefix, "/") {
		t.Errorf("branch_prefix %q is not a directory-style prefix", c.BranchPrefix)
	}
	want := c.BranchPrefix + c.BranchExample.Epic + "/" + c.BranchExample.Tick
	if c.BranchExample.Branch != want {
		t.Errorf("branch example is %q; prefix + epic + tick is %q", c.BranchExample.Branch, want)
	}
	if !strings.Contains(c.ResultFileExample.Path, c.ResultFileExample.Tick) {
		t.Errorf("the RESULT path %q does not carry the tick id %q — a shared filename collides "+
			"when a wave is merged", c.ResultFileExample.Path, c.ResultFileExample.Tick)
	}
	if !strings.HasSuffix(c.ResultFileExample.Path, ".md") {
		t.Errorf("the RESULT path %q is not a markdown report", c.ResultFileExample.Path)
	}
	if strings.Contains(c.ResultFileExample.Path, "/") {
		t.Errorf("the RESULT path %q is not at the repository root; the executor owns where it lands",
			c.ResultFileExample.Path)
	}
}

// The boundary half: a worker has no tk, and an attempt is REPORTED. Appendix A
// #10 is the same rule from the other side, and both must say so.
func TestTheWorkerBoundaryIsEnforcedAndReported(t *testing.T) {
	var c workerBoot
	readContract(t, workerBootFile, &c)

	if c.Boundary.TkDenied == "" {
		t.Error("the contract does not say that tk is denied to a worker agent")
	}
	if !strings.Contains(strings.ToLower(c.Boundary.TkDenied), "tk") {
		t.Errorf("the denial message no longer names tk: %q", c.Boundary.TkDenied)
	}
	if c.Boundary.ReportMarker == "" {
		t.Error("a refused write that is not reported is a boundary nobody hears about")
	}
	if c.WorkerActor == "" {
		t.Error("the worker has no actor identity, so nothing it did can be attributed")
	}
}

// Every environment variable and every exit code is distinct. Two outcomes
// sharing an exit code is A9's failure in the smallest possible form: the
// caller cannot tell which happened.
func TestEnvAndExitCodesAreDistinct(t *testing.T) {
	var c workerBoot
	readContract(t, workerBootFile, &c)

	if len(c.Env) == 0 {
		t.Fatal("the contract passes nothing to the worker")
	}
	seen := map[string]string{}
	for key, name := range c.Env {
		if name == "" {
			t.Errorf("env %q maps to an empty variable name", key)
			continue
		}
		if !strings.HasPrefix(name, "TICKS_") {
			t.Errorf("env %q is %q; the worker's variables are namespaced", key, name)
		}
		if other, ok := seen[name]; ok {
			t.Errorf("%q is used for both %q and %q", name, other, key)
		}
		seen[name] = key
	}

	if len(c.ExitCodes) == 0 {
		t.Fatal("the worker publishes no exit codes, so every failure looks alike")
	}
	byCode := map[int]string{}
	for name, code := range c.ExitCodes {
		if code < 2 {
			t.Errorf("exit code %d for %q collides with success or a generic failure", code, name)
		}
		if other, ok := byCode[code]; ok {
			t.Errorf("exit code %d means both %q and %q", code, other, name)
		}
		byCode[code] = name
	}

	// setup has exactly two modes and they are not the same word.
	if len(c.SetupModes) != 2 {
		t.Errorf("setup has %d modes; the contract names always and skip", len(c.SetupModes))
	}
	values := map[string]bool{}
	for _, v := range c.SetupModes {
		if values[v] {
			t.Errorf("two setup modes share the value %q", v)
		}
		values[v] = true
	}
}
