//go:build !windows

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The boot and finish phases (epic 43y, tick pom — the pi-durable worker
// host's half of worker.sh's own contract; docs/spikes/
// n0b-round2-pi-durable.md, "The worker contract on pi-durable").
//
// The all-in-one default is what the whole existing suite drives; these
// tests drive the SPLIT: `--boot` as the env's first command, and `--finish`
// as what the host runs once the conversation settles, with the
// conversation's outcome as its argument. What is under test is exactly what
// the tick's mapping promises:
//
//   - the boot's faults are the all-in-one's OWN classes (2-8, 13-15) —
//     the boot-fault codes #171/#178 are preserved through the new door;
//   - the boot's success hands the host the prompt and the two names it
//     cannot derive (the branch, the report path), and records the branch
//     for the finish phase's own process;
//   - the finish phase delivers the durable layer exactly as the all-in-one
//     does — boundary notes, sweep, salvage, fallback report, container
//     facts, commit, push — and decides the SAME exit codes 9/10/11 from the
//     same git facts.
//
// The "agent" between the two phases is simulated the way the harness stub
// simulates one: real commits and a real report written into the real
// checkout the boot cloned, because what is under test is what the FINISH
// does with whatever the conversation left behind.

// phasesState gives the fixture's workers their own state dir, the way the
// dispatcher does: the boot and the finish are two PROCESSES sharing one
// container, and the state dir is the one place the second finds what the
// first left.
func (f *workerFixture) phasesState(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(f.root, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.env[EnvWorkerStateDir] = dir
	return dir
}

// simulateAgent is the conversation's footprint: the commits and the report
// a settled worker conversation leaves in the checkout the boot made.
func (f *workerFixture) simulateAgent(t *testing.T, work, report string) {
	t.Helper()
	if work != "" {
		write(t, filepath.Join(f.workdir, "worked.txt"), work)
		git(t, f.workdir, "add", "worked.txt")
		git(t, f.workdir, "commit", "-q", "-m", "tick "+f.tick+": the work")
	}
	if report != "" {
		write(t, filepath.Join(f.workdir, WorkerResultFile(f.tick)), report)
	}
}

func TestWorkerBootPhaseHandsOffTheConversation(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	state := f.phasesState(t)

	out, code := f.run(WorkerBootArg)
	if code != 0 {
		t.Fatalf("a healthy boot exited %d:\n%s", code, out)
	}
	// The marker line carries the two names the host cannot derive, exactly
	// as the contract pins them: the branch (adoption can rename it) and the
	// report path.
	mustContain(t, out, WorkerBootMarker+" branch="+WorkerBranch(f.epic, f.tick)+" result="+WorkerResultFile(f.tick),
		"the boot marker line with the branch and the report path")
	// The prompt follows between the two markers, whole and verbatim — what
	// the host submits to the conversation as its input.
	begin := strings.Index(out, WorkerBootPromptBegin+"\n")
	end := strings.LastIndex(out, WorkerBootPromptEnd)
	if begin == -1 || end == -1 || end < begin {
		t.Fatalf("the boot's handoff does not carry the prompt between its markers:\n%s", out)
	}
	prompt := out[begin+len(WorkerBootPromptBegin)+1 : end]
	if prompt != "implement the tick\n" {
		t.Errorf("the prompt between the markers is %q, want the rendered worker prompt", prompt)
	}
	// The boot is the env's FIRST command: no harness ran, so the
	// conversation owns every model call after it.
	if f.harnessRecord() != "" {
		t.Error("the boot phase ran the harness; the conversation the host runs owns those calls")
	}
	// The branch the boot resolved is where the finish phase's own process
	// will find it.
	recorded, err := os.ReadFile(filepath.Join(state, "branch"))
	if err != nil || strings.TrimSpace(string(recorded)) != WorkerBranch(f.epic, f.tick) {
		t.Errorf("the state dir records the branch as %q (%v), want %s", string(recorded), err, WorkerBranch(f.epic, f.tick))
	}
	// And the boot pushed nothing: an empty worker branch is what collect's
	// verdicts are keyed on, and a boot that pushed would look like work.
	if exists, _ := f.remoteBranch(WorkerBranch(f.epic, f.tick)); exists {
		t.Error("the boot phase pushed the worker branch; only the finish phase delivers")
	}
}

func TestWorkerBootPhaseDiesWithTheAllInOneBootFaultCodes(t *testing.T) {
	shorttest.EndToEnd(t)
	for name, tc := range map[string]struct {
		env   map[string]string
		unset []string
		code  int
	}{
		"no tick":        {unset: []string{EnvTick}, code: ExitConfig},
		"bad setup mode": {env: map[string]string{"TICKS_WORKER_SETUP": "banana"}, code: ExitConfig},
		"setup fails":    {env: map[string]string{"TICKS_TEST_SANDBOX_SETUP_EXIT": "1"}, code: ExitSetup},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.phasesState(t)
			for k, v := range tc.env {
				f.env[k] = v
			}
			for _, k := range tc.unset {
				delete(f.env, k)
			}
			out, code := f.run(WorkerBootArg)
			if code != tc.code {
				t.Fatalf("the boot fault exited %d, want the all-in-one's own %d:\n%s", code, tc.code, out)
			}
			if strings.Contains(out, WorkerBootMarker) {
				t.Error("a boot that died printed the boot marker; a half-booted container must not look booted")
			}
		})
	}
}

// The boot-fault classes #171/#178 are preserved through the split: a boot
// that stops before the conversation still leaves its reason on origin, the
// way the all-in-one's EXIT trap always did.
func TestWorkerBootPhaseStillPushesItsBootStopReason(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	f.env["TICKS_TEST_SANDBOX_SETUP_EXIT"] = "1"
	out, code := f.run(WorkerBootArg)
	if code != ExitSetup {
		t.Fatalf("the boot fault exited %d, want %d:\n%s", code, ExitSetup, out)
	}
	marker := WorkerBranch(f.epic, f.tick) + "-boot-stopped"
	exists, _ := f.remoteBranch(marker)
	if !exists {
		t.Fatalf("the boot-stopped marker branch %s is not on origin; the run could only read this stop as a container that answered nothing:\n%s", marker, out)
	}
	file := "BOOT-STOPPED-" + f.tick + ".md"
	body, ok := f.remoteFile(marker, file)
	if !ok {
		t.Fatalf("the marker branch carries no %s", file)
	}
	mustContain(t, body, "exit: 6", "the boot's exit code, the class collect keys on")
	mustContain(t, body, "reason: ", "the boot's own words for why it stopped")
}

// The finish phase delivers what the conversation left behind, exactly as
// the all-in-one delivers it: the same commits, the same prepended facts,
// the same report on origin, and exit 0 for a delivered tick.
func TestWorkerFinishPhaseDeliversTheConversation(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot exited %d:\n%s", code, out)
	}
	f.simulateAgent(t, "work\n", "# "+f.tick+"\n\nI did the thing.\n\nSTATUS: DONE\n")

	out, code := f.run(WorkerFinishArg, "0")
	if code != 0 {
		t.Fatalf("a finish over delivered work exited %d:\n%s", code, out)
	}
	branch := WorkerBranch(f.epic, f.tick)
	exists, commits := f.remoteBranch(branch)
	if !exists {
		t.Fatalf("origin has no %s:\n%s", branch, out)
	}
	if commits != 2 {
		t.Errorf("%s carries %d commit(s) beyond the base, want 2 (the conversation's work and the report)", branch, commits)
	}
	if _, ok := f.remoteFile(branch, "worked.txt"); !ok {
		t.Error("the conversation's work is not on the pushed branch")
	}
	report, ok := f.remoteFile(branch, WorkerResultFile(f.tick))
	if !ok {
		t.Fatal("the report is not committed on the pushed branch; collect reads it off the branch")
	}
	mustContain(t, report, "STATUS: DONE", "the agent's own verdict")
	mustContain(t, report, "ticks-worker", "the container facts the finish prepends")
	if idx := strings.LastIndex(report, "STATUS:"); idx == -1 || !strings.Contains(report[idx:], "DONE") {
		t.Errorf("the finish's annotation displaced the agent's status line:\n%s", report)
	}
}

// The finish phase decides the all-in-one's OWN exit codes from the same git
// facts: 10 for a branch that carries no work, 11 for an outcome that was
// not a clean settled run.
func TestWorkerFinishPhaseDecidesTheAllInOneExitCodes(t *testing.T) {
	shorttest.EndToEnd(t)
	for name, tc := range map[string]struct {
		work, report, status string
		code                 int
	}{
		"clean exit, no work, a report": {"", "# tap\n\nSTATUS: DONE\n", "0", ExitWorkerNoWork},
		"aborted conversation":          {"", "", "1", ExitWorkerAgent},
		"clean exit, no report at all":  {"", "", "0", ExitWorkerAgent},
		"clean exit with work":          {"work\n", "# tap\n\nSTATUS: DONE\n", "0", 0},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.phasesState(t)
			if out, code := f.run(WorkerBootArg); code != 0 {
				t.Fatalf("the boot exited %d:\n%s", code, out)
			}
			f.simulateAgent(t, tc.work, tc.report)

			out, code := f.run(WorkerFinishArg, tc.status)
			if code != tc.code {
				t.Fatalf("the finish decided exit %d, want the all-in-one's own %d:\n%s", code, tc.code, out)
			}
			// Whatever the outcome, the durable layer got the branch and the
			// report — that is the phase's whole contract.
			branch := WorkerBranch(f.epic, f.tick)
			if _, ok := f.remoteFile(branch, WorkerResultFile(f.tick)); !ok {
				t.Error("the report was not pushed, so the tick's outcome reached no one")
			}
		})
	}
}

// The finish phase runs in its own process: the branch it pushes is the one
// the boot RESOLVED, not one it re-derives — adoption can rename the branch,
// and a re-derivation would push beside the work instead of over it.
func TestWorkerFinishPhasePushesTheBranchTheBootAdopted(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	// An earlier attempt's work on origin at the same base: the boot ADOPTS
	// it rather than starting over.
	branch := WorkerBranch(f.epic, f.tick)
	git(t, f.source, "checkout", "-q", "-b", branch)
	write(t, filepath.Join(f.source, "earlier.txt"), "earlier\n")
	git(t, f.source, "add", "earlier.txt")
	git(t, f.source, "commit", "-q", "-m", "tick "+f.tick+": an earlier attempt")
	git(t, f.source, "checkout", "-q", "main")

	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot exited %d:\n%s", code, out)
	}
	f.simulateAgent(t, "work\n", "# "+f.tick+"\n\nSTATUS: DONE\n")
	out, code := f.run(WorkerFinishArg, "0")
	if code != 0 {
		t.Fatalf("the finish exited %d:\n%s", code, out)
	}
	// The adopted branch now carries the earlier attempt's commit too: the
	// finish pushed the branch the boot recorded, and collect keeps every
	// attempt's evidence.
	_, commits := f.remoteBranch(branch)
	if commits < 3 {
		t.Errorf("%s carries %d commit(s), want at least 3 (the adopted attempt's, this conversation's work, the report)", branch, commits)
	}
	if _, ok := f.remoteFile(branch, "earlier.txt"); !ok {
		t.Error("the finish did not push the branch the boot adopted; the earlier attempt's work would be overwritten by the next one")
	}
}

// A finish without a boot finds no checkout and no branch record: the
// clone class, the same class the all-in-one gives a boot that could not
// make its checkout — a finish cannot invent a branch to push.
func TestWorkerFinishPhaseRefusesABootItNeverHad(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	// No --boot: no checkout, no branch record.
	out, code := f.run(WorkerFinishArg, "0")
	if code != ExitClone {
		t.Fatalf("a finish with no boot exited %d, want %d (the clone class):\n%s", code, ExitClone, out)
	}
}

// The finish's one argument is the conversation's outcome as an exit status;
// anything else is a host bug, refused as a config fault rather than read as
// a clean exit.
func TestWorkerFinishPhaseRefusesANonNumericOutcome(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot exited %d:\n%s", code, out)
	}
	out, code := f.run(WorkerFinishArg, "aborted")
	if code != ExitConfig {
		t.Fatalf("a finish handed a non-numeric outcome exited %d, want %d:\n%s", code, ExitConfig, out)
	}
}

// The all-in-one default is untouched: the same fixture that drives the
// phases drives the no-arg entrypoint end to end, so the split cannot have
// broken the door every live container uses today.
func TestWorkerAllInOneDefaultStillRunsTheWholeContract(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	out, code := f.run()
	if code != 0 {
		t.Fatalf("the all-in-one default exited %d:\n%s", code, out)
	}
	branch := WorkerBranch(f.epic, f.tick)
	exists, commits := f.remoteBranch(branch)
	if !exists || commits != 2 {
		t.Fatalf("the all-in-one delivered %s with %d commit(s), want 2:\n%s", branch, commits, out)
	}
	if f.harnessRecord() == "" {
		t.Error("the all-in-one default did not run the harness")
	}
	_ = exec.Command("true") // keep os/exec imported for the fixture helpers above
}

// ----------------------------------------------------------- the setup entry ---

// `ticks-worker --setup` (epic 43y, tick i3h): the entry the pi-durable
// host's RESTORE runs. A container lost mid-turn is rebuilt from the last wip
// snapshot by the host's own git plumbing (harness/src/workspace/
// checkpoints.ts `restoreWorkspace`), which can clone and check out but
// cannot re-run the one boot step the restored tree still needs: the
// repository's own [sandbox] setup, whose dependency installs died with the
// container. This entry is that step alone — and nothing else around it: no
// clone, no harness, no push, and none of the boot's inputs (a restored box
// has no TICKS_TICK of its own).
func TestWorkerSetupEntryRunsTheRepositorySetupAndNothingElse(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot that made the checkout exited %d:\n%s", code, out)
	}
	// The container the host restored: the checkout is there, everything the
	// boot resolved is not — the entry must take none of it.
	delete(f.env, EnvTick)
	os.Remove(f.ticfacRecord)

	out, code := f.run(WorkerSetupArg)
	if code != 0 {
		t.Fatalf("the setup entry exited %d:\n%s", code, out)
	}
	mustContain(t, f.ticfacCalls(), "sandbox setup", "the repository's own setup, re-run")
	mustContain(t, out, "repository setup took", "the per-boot cost of the step fan-out pays N times")
	if f.harnessRecord() != "" {
		t.Error("the setup entry ran the harness; the conversation the host runs owns those calls")
	}
	// Pushed nothing: the attempt branch is the conversation's and collect's
	// to write, and a setup entry that pushed would read as work.
	if exists, _ := f.remoteBranch(WorkerBranch(f.epic, f.tick)); exists {
		t.Error("the setup entry pushed the worker branch; the restore hands the branch back to the conversation, not to origin")
	}
}

// The restored box gets the toolchain the repository declares, not just
// its setup (epic 43y, tick r2m): the boot provisions a declared toolchain
// (image/common.sh `provision_toolchain`) before it runs the repository's
// setup, and a container lost mid-turn takes those installs with it — the
// restored container is rebuilt from the image, which may not carry what
// the tree declares (mise.toml, .tool-versions). A restore that re-ran only
// the setup handed the conversation a tree whose tests could not run.
func TestWorkerSetupEntryProvisionsTheDeclaredToolchain(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot that made the checkout exited %d:\n%s", code, out)
	}
	// The tree the snapshot restores: a repository that declares a toolchain
	// the image does not satisfy. Written after the boot so the boot's own
	// provisioning is not the thing under test.
	write(t, filepath.Join(f.workdir, "mise.toml"), "[tools]\nnode = '22'\n")
	// The container the host restored: the checkout is there, everything the
	// boot resolved is not — and the version manager's record starts empty.
	delete(f.env, EnvTick)
	f.env["TICKS_TEST_MISE_RECORD"] = f.miseRecord
	os.Remove(f.ticfacRecord)

	out, code := f.run(WorkerSetupArg)
	if code != 0 {
		t.Fatalf("the setup entry exited %d:\n%s", code, out)
	}
	if calls := f.miseCalls(); !strings.Contains(calls, "install") {
		t.Errorf("the restored box provisioned no declared toolchain; the version manager was asked:\n%s", calls)
	}
	mustContain(t, out, "the repository declares a toolchain", "the provisioning the boot ran, re-run by the restore")
	mustContain(t, f.ticfacCalls(), "sandbox setup", "the repository's own setup, still run after the toolchain")
	// Toolchain BEFORE setup, the boot's order: a repository's setup can need
	// the very tools the tree declares.
	if p, s := strings.Index(out, "the repository declares a toolchain"), strings.Index(out, "repository setup took"); p < 0 || s < 0 || p > s {
		t.Errorf("the setup entry did not provision the declared toolchain before the repository's setup")
	}
}

// A box with no checkout is not a restored workspace: the setup entry runs
// only after the boot or the host's restore made one, and it refuses as the
// clone class rather than provisioning a checkout of its own.
func TestWorkerSetupEntryRefusesABoxWithNoCheckout(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	out, code := f.run(WorkerSetupArg)
	if code != ExitClone {
		t.Fatalf("a setup entry with no checkout exited %d, want %d (the clone class):\n%s", code, ExitClone, out)
	}
}

// The setup's own fault class is the boot's: a repository whose setup fails
// in a restored container is the same wave-killing fault it was at boot.
func TestWorkerSetupEntryDiesWithTheSetupFaultCode(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot that made the checkout exited %d:\n%s", code, out)
	}
	f.env["TICKS_TEST_SANDBOX_SETUP_EXIT"] = "1"
	out, code := f.run(WorkerSetupArg)
	if code != ExitSetup {
		t.Fatalf("the setup entry exited %d, want %d:\n%s", code, ExitSetup, out)
	}
}

// The wave's setup lever still holds through the restore: a wave dispatched
// with TICKS_WORKER_SETUP=skip skipped the install at boot, and the box a
// lost container restored into keeps skipping it.
func TestWorkerSetupEntryHonoursTheWaveSetupLever(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.env[EnvWorkerSetup] = WorkerSetupSkip
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot that made the checkout exited %d:\n%s", code, out)
	}
	os.Remove(f.ticfacRecord)
	out, code := f.run(WorkerSetupArg)
	if code != 0 {
		t.Fatalf("the skipped setup entry exited %d:\n%s", code, out)
	}
	if strings.Contains(f.ticfacCalls(), "sandbox setup") {
		t.Error("TICKS_WORKER_SETUP=skip still ran the repository's setup in the restored box")
	}
	mustContain(t, out, "does NOT run the repository's [sandbox] setup", "what skipping costs")
}

// An unknown setup mode is a config fault through this door too, never a
// silent `always`: the value arrives from the host's record of the boot env,
// and one that never validated would re-provision every restore.
func TestWorkerSetupEntryRefusesAnUnknownSetupMode(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.phasesState(t)
	if out, code := f.run(WorkerBootArg); code != 0 {
		t.Fatalf("the boot that made the checkout exited %d:\n%s", code, out)
	}
	f.env[EnvWorkerSetup] = "maybe"
	out, code := f.run(WorkerSetupArg)
	if code != ExitConfig {
		t.Fatalf("the setup entry exited %d, want %d:\n%s", code, ExitConfig, out)
	}
}
