package cli

// `ticfac run <epic>` (tick 9sz): the acceptance criteria, as tests.
//
//   - in a repo with herdr running, one command starts the epic in herdr
//     panes with no flag, env var or path — the herdr set EMBEDDED in the
//     binary is what reaches the panes;
//   - a second invocation attaches, a stopped run resumes;
//   - Ctrl-C detaches without stopping;
//   - the token is fetched from gh only when the close-out needs it (hio's
//     tests hold that gate; here the note that SAYS so is pinned in
//     forge_test.go);
//   - run-epic still works (its own suite; this suite spawns it).
//
// The seams answer the herdr probe, the detached spawn and the attach for
// the argv-and-decision tests, exactly as the evacuation test's tracker seam
// answers the tracker (tick ppt): everything above each seam is the
// production code the criterion is about. The one test that runs the
// production values of all three proves the property no seam can — that the
// child this command starts actually SURVIVES the terminal that started it.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// runDetachedChild is the child's whole job in the end-to-end test below:
// claim the run's life the way a real run-epic does (runlife.Claim), say one
// feed line so the attach has the run's own word to show, and stay alive
// until the test says stop. It runs no reconciler — what is under test is
// the START and its detach semantics, and the reconciler's own resume path
// is proven by internal/reconcile's suite and the evacuation test.
func runDetachedChild(args []string) int {
	var epicID, repo string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo":
			if i+1 < len(args) {
				repo = args[i+1]
			}
			i++
		case strings.HasPrefix(args[i], "--"):
			i++ // --profiles X, --wall N: a flag and its value
		default:
			if epicID == "" {
				epicID = args[i]
			}
		}
	}
	if epicID == "" || repo == "" {
		fmt.Fprintln(os.Stderr, "run child: no epic id or repo to claim")
		return 2
	}
	runID := "epic-" + epicID
	life, err := runlife.Claim(repo, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run child: %v\n", err)
		return 1
	}
	defer life.Release("the run child stopped")
	if err := runfeed.Open(repo, runID).Append(runfeed.NewEvent(
		time.Now(), runID, "", nil, reconcile.StageResumed,
		"the detached child claimed this run for the run command's test")); err != nil {
		fmt.Fprintf(os.Stderr, "run child: %v\n", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	select {
	case <-sig:
	case <-time.After(10 * time.Minute): // never in a healthy test; a leak's end
	}
	return 0
}

// --- the seams' fakes -------------------------------------------------

// saveRunSeams snapshots the three seams and restores them on cleanup, so a
// test that answers one with a controlled value cannot leak it into the
// next test — the same discipline the tracker seam and doctor's probes
// follow.
func saveRunSeams(t *testing.T) {
	t.Helper()
	savedHerdr, savedSpawn, savedAttach := runHerdrLive, runStartDetached, runAttach
	t.Cleanup(func() {
		runHerdrLive, runStartDetached, runAttach = savedHerdr, savedSpawn, savedAttach
	})
}

// fakeChild is a runChild the test controls.
type fakeChild struct {
	exited bool
	code   int
	pid    int
}

func (c *fakeChild) Exited() bool  { return c.exited }
func (c *fakeChild) ExitCode() int { return c.code }
func (c *fakeChild) Pid() int      { return c.pid }

// spawnRecorder records what the spawn seam was asked to start. Tests in
// this package are sequential (none of this file's tests call t.Parallel),
// so no lock is needed — the same reasoning cli_test's helpers rest on.
type spawnRecorder struct {
	argvs [][]string
}

// claimingSpawn simulates the child's first act synchronously: it claims the
// run's life the way a real run-epic does, from the very argv it was asked
// to start, so the attach wait finds the claim without a single real
// process. The claim names the TEST process, which is alive, so the probe
// answers honestly.
func claimingSpawn(t *testing.T, rec *spawnRecorder) func([]string, io.Writer) (runChild, error) {
	return func(argv []string, out io.Writer) (runChild, error) {
		rec.argvs = append(rec.argvs, argv)
		epicID, repo := "", ""
		for i := 1; i < len(argv); i++ {
			if argv[i] == "--repo" && i+1 < len(argv) {
				repo, epicID = argv[i+1], argv[1]
			}
		}
		life, err := runlife.Claim(repo, "epic-"+epicID)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { life.Release("the test's fake claim") })
		return &fakeChild{pid: os.Getpid()}, nil
	}
}

// attachRecorder records what the attach seam was asked to watch, and
// answers the code the test wants.
type attachRecorder struct {
	mu     sync.Mutex
	called []string // the run ids attach was asked for
	code   int
}

func (r *attachRecorder) seam(ctx context.Context, repo, runID string, stdout, stderr io.Writer) int {
	r.mu.Lock()
	r.called = append(r.called, runID)
	code := r.code
	r.mu.Unlock()
	return code
}

// runBody drives the command body the way cobra does: flags parsed off the
// same declaration the command carries, the positionals handed to the body.
// The ctx is the caller's, so a test can detach the way a terminal's Ctrl-C
// does — by cancelling it — without reaching for real signals.
func runBody(ctx context.Context, t *testing.T, args []string, stdout, stderr io.Writer) int {
	t.Helper()
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fl := defineRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return runCommand(ctx, fs.Args(), fl, stdout, stderr)
}

// herdrAnswers installs a herdr probe seam with a controlled answer.
func herdrAnswers(t *testing.T, detail string, err error) *bool {
	t.Helper()
	called := false
	runHerdrLive = func(context.Context, string) (string, error) {
		called = true
		return detail, err
	}
	return &called
}

// --- the criteria ------------------------------------------------------

// short: the seams answer the herdr probe, the spawn and the attach; the
// only real subprocess is runlife's own ps, which the short suite already
// runs for every probe it makes.

// A live herdr means the run selects the embedded herdr profile set: no
// filesystem path anywhere in the argv, and the virtual name is the only
// new thing the run is told. The set is split, not uniform (tick 2q5):
// panes host the frontier rung, implement workers run headless on the
// pi-durable harness, and the starter's prose says so.
func TestRunStartsInHerdrPanesWithTheEmbeddedProfileSet(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	herdrAnswers(t, "herdr 9.9.9 answers at /tmp/herdr.sock", nil)
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	attach := &attachRecorder{}
	runAttach = attach.seam

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	want := []string{"run-epic", "foo", "--repo", filepath.Join(repo), "--profiles", profile.EmbeddedHerdr}
	if len(rec.argvs) != 1 {
		t.Fatalf("the spawn was asked %d times, want once", len(rec.argvs))
	}
	if got := rec.argvs[0]; !equalArgv(got, want) {
		t.Errorf("the background run was started as %v, want %v — no flag, env var or path beyond the embedded set's own name", got, want)
	}
	if !strings.Contains(stdout.String(), "herdr 9.9.9 answers") ||
		!strings.Contains(stdout.String(), "profile set embedded in this binary") {
		t.Errorf("the start does not say it is dispatching with the embedded herdr set: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "pi-durable") {
		t.Errorf("the start does not say where implement workers run: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "starting it in the background") {
		t.Errorf("a run that never ran says nothing about starting: %q", stdout.String())
	}
	if len(attach.called) != 1 || attach.called[0] != "epic-foo" {
		t.Errorf("the attach was asked for %v, want the run id epic-foo", attach.called)
	}
}

// No live herdr is not an error: the run dispatches with the local set
// embedded as the binary's default, and says which probe failed.
func TestRunWithoutHerdrUsesTheBinarysDefaultProfiles(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	herdrAnswers(t, "", fmt.Errorf("herdr does not answer at /tmp/herdr.sock"))
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam

	var stdout, stderr syncBuffer
	if code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	for _, arg := range rec.argvs[0] {
		if arg == "--profiles" || strings.HasPrefix(arg, "--profiles") {
			t.Errorf("a run with no live herdr was given a profile set: %v", rec.argvs[0])
		}
	}
	if !strings.Contains(stdout.String(), "no live herdr") {
		t.Errorf("the start does not say the probe found no herdr: %q", stdout.String())
	}
}

// --no-herdr opts out of the PROBE itself: an operator who knows panes are
// not wanted does not pay the dial, and the run gets the binary's default
// set.
func TestRunNoHerdrOptsOutOfTheProbeItself(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	called := herdrAnswers(t, "herdr 9.9.9 answers at /tmp/herdr.sock", nil)
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam

	var stdout, stderr syncBuffer
	if code := runBody(context.Background(), t, []string{"--repo", repo, "--no-herdr", "foo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if *called {
		t.Error("--no-herdr still dialled the herdr probe")
	}
	if strings.Contains(strings.Join(rec.argvs[0], " "), "--profiles") {
		t.Errorf("--no-herdr still reached for a profile set: %v", rec.argvs[0])
	}
	if !strings.Contains(stdout.String(), "herdr skipped (--no-herdr)") {
		t.Errorf("the start does not say herdr was skipped: %q", stdout.String())
	}
}

// --profiles stays as the expert override, forwarded verbatim — and the
// herdr probe is skipped, because its answer cannot change what is
// dispatched. --wall rides along as the opt-in backstop and nothing else
// does.
func TestRunForwardsTheProfilesOverrideAndTheOptInWall(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	called := herdrAnswers(t, "herdr 9.9.9 answers at /tmp/herdr.sock", nil)
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t,
		[]string{"--repo", repo, "--profiles", filepath.Join(repo, "expert"), "--wall", "3600", "foo"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if *called {
		t.Error("the herdr probe ran although the operator named a profile set")
	}
	want := []string{"run-epic", "foo", "--repo", filepath.Join(repo), "--profiles", filepath.Join(repo, "expert"), "--wall", "3600"}
	if !equalArgv(rec.argvs[0], want) {
		t.Errorf("the background run was started as %v, want %v", rec.argvs[0], want)
	}
}

// Unnamed, no wall clock is injected: the argv carries none. The reconciler's
// own documented per-job bound is what then applies — a flag this command
// did not add is not one it can speak for.
func TestRunInjectsNoWallClockWhenNoneIsNamed(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam
	runHerdrLive = func(context.Context, string) (string, error) { return "", fmt.Errorf("none") }

	var stdout, stderr syncBuffer
	if code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	for _, arg := range rec.argvs[0] {
		if strings.HasPrefix(arg, "--wall") {
			t.Errorf("an unnamed wall clock reached the run: %v", rec.argvs[0])
		}
	}
}

// The production herdr probe itself, against the one canonical fake
// server: a live socket is DETECTED through the same resolution the run
// dials ($HERDR_SOCKET_PATH when the repo pins no orchestration.socket),
// answers with the server's own version, and the run it starts is given the
// embedded herdr profile set — the acceptance's "in a repo with herdr
// running" with a live socket under it, everything above the probe real
// too. A closed socket is the same probe's honest negative.
func TestRunDetectsALiveHerdrThroughTheRealSocket(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	srv := herdtest.New(t, herdtest.Config{Version: "9.9.9"})
	t.Setenv("HERDR_SOCKET_PATH", srv.Path())
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam

	var stdout, stderr syncBuffer
	if code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "herdr 9.9.9 answers") {
		t.Errorf("the live herdr was not reported with its version: %q", stdout.String())
	}
	if !equalArgv(rec.argvs[0],
		[]string{"run-epic", "foo", "--repo", filepath.Join(repo), "--profiles", profile.EmbeddedHerdr}) {
		t.Errorf("a run with a live herdr socket did not get the embedded herdr set: %v", rec.argvs[0])
	}

	// The server gone is "no live herdr": the run dispatches with the
	// binary's default set and says which socket stayed silent.
	srv.Close()
	// A second epic: the first fake claim still stands (it is released at
	// the test's end), and a live run attaches rather than starting one.
	rec2 := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec2)
	stdout.Reset()
	if code := runBody(context.Background(), t, []string{"--repo", repo, "bar"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	for _, arg := range rec2.argvs[0] {
		if strings.HasPrefix(arg, "--profiles") {
			t.Errorf("a dead socket still reached for the herdr set: %v", rec2.argvs[0])
		}
	}
	if !strings.Contains(stdout.String(), "no live herdr") {
		t.Errorf("the dead socket is not said: %q", stdout.String())
	}
}

// A live run is ATTACHED, not restarted: no spawn at all, and the line says
// so — the second invocation of the one command.
func TestRunAttachesALiveRunWithoutStartingAnother(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-foo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { life.Release("the test's claim") })
	herdrAnswers(t, "herdr 9.9.9 answers at /tmp/herdr.sock", nil) // live herdr must not matter on attach
	runStartDetached = func(argv []string, out io.Writer) (runChild, error) {
		t.Errorf("a live run was started again: %v", argv)
		return &fakeChild{}, nil
	}
	attach := &attachRecorder{}
	runAttach = attach.seam

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if len(attach.called) != 1 || attach.called[0] != "epic-foo" {
		t.Errorf("the attach was asked for %v, want the live run epic-foo", attach.called)
	}
	if !strings.Contains(stdout.String(), "run epic-foo is alive") ||
		!strings.Contains(stdout.String(), "attaching; Ctrl-C detaches without stopping it") {
		t.Errorf("the invocation does not say it is attaching to the live run: %q", stdout.String())
	}
}

// A DEAD run — a pidfile naming a process that is gone, the shape a run
// leaves when it died without saying so — is resumed, not treated as
// something to be careful about: the reconciler's boot path decides what a
// resume is, from the state on the integration branch.
func TestRunResumesARunThatDiedWithoutReleasing(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	dir := runlife.Dir(repo, "epic-foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pid that cannot exist, with a start time to match: the probe reads
	// Dead — the run died and its pid was never released.
	if err := os.WriteFile(filepath.Join(dir, runlife.PIDName), []byte(
		`{"schema_version": 2, "run_id": "epic-foo", "pid": 999999, "process_start": "never",
		  "started_at": "2026-09-27T00:00:00Z", "host": "gone.example.com"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	runAttach = (&attachRecorder{}).seam
	runHerdrLive = func(context.Context, string) (string, error) { return "", fmt.Errorf("none") }

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "resuming it in the background") {
		t.Errorf("a run that ran and died is not said to be resumed: %q", stdout.String())
	}
	if len(rec.argvs) != 1 {
		t.Fatalf("the dead run was started %d times, want once", len(rec.argvs))
	}
}

// Ctrl-C detaches and never stops: the attach ending because the invocation
// was interrupted (its context, which is what a terminal's Ctrl-C cancels)
// while the run is alive is a DETACH — exit 0, and the line names the one
// command that comes back.
func TestRunTreatsAnInterruptedAttachAsADetach(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-foo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { life.Release("the test's claim") })

	ctx, cancel := context.WithCancel(context.Background())
	// The attach seam ends the way a real one does — BECAUSE the invocation
	// was interrupted, watch's own interrupted code in hand — so the detach
	// is what the command under test decides, never a race between the test's
	// cancel and the seam's return.
	runAttach = func(ctx context.Context, repo, runID string, stdout, stderr io.Writer) int {
		<-ctx.Done()
		return 1
	}
	runStartDetached = func(argv []string, out io.Writer) (runChild, error) {
		t.Errorf("the attach path started a run: %v", argv)
		return &fakeChild{}, nil
	}

	var stdout, stderr syncBuffer
	codec := make(chan int, 1)
	go func() { codec <- runBody(ctx, t, []string{"--repo", repo, "foo"}, &stdout, &stderr) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "attaching") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel() // the terminal's Ctrl-C, at the moment the attach is open
	var code int
	select {
	case code = <-codec:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted attach never returned")
	}
	if code != exitRunning {
		t.Errorf("an interrupted attach of a live run exited %d, want %d — detaching is not a failure and not done either: "+
			"the run keeps going, and the exit table's running class is how a script tells (tick 8v3)", code, exitRunning)
	}
	if !strings.Contains(stdout.String(), "detached from run epic-foo") ||
		!strings.Contains(stdout.String(), "keeps going in the background") {
		t.Errorf("the detach does not say the run keeps going: %q", stdout.String())
	}
	// The run was never stopped: the claim the test holds still stands.
	if probe := runlife.Probe(repo, "epic-foo", time.Now()); probe.State != runlife.Alive {
		t.Errorf("the run is %s after a detach, want alive: %s", probe.State, probe.Reason)
	}
}

// A child that exits without claiming the run is relayed, not left as a
// mystery: its own words from start.log, and its own exit code. This is the
// path `ticfac run` answers a fail-closed run-epic refusal through — the
// refusal must reach the operator, not vanish with the child.
func TestRunRelaysAChildThatExitedWithoutClaiming(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	rec := &spawnRecorder{}
	runStartDetached = func(argv []string, out io.Writer) (runChild, error) {
		rec.argvs = append(rec.argvs, argv)
		// The refusal a build with no executor writes, the shape
		// TestRunEpicFailsClosed pins, in the child's own register.
		fmt.Fprintf(out, "ticfac run-epic foo: %s.\n", NoExecutorMessage)
		return &fakeChild{exited: true, code: ExitNoExecutor}, nil
	}
	attach := &attachRecorder{}
	runAttach = attach.seam
	runHerdrLive = func(context.Context, string) (string, error) { return "", fmt.Errorf("none") }

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != ExitNoExecutor {
		t.Errorf("exit %d, want the child's own %d", code, ExitNoExecutor)
	}
	if !strings.Contains(stderr.String(), NoExecutorMessage) {
		t.Errorf("the child's refusal was not relayed: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "exited 2 without claiming the run") {
		t.Errorf("the relay does not say the child left without a claim: %q", stderr.String())
	}
	if len(attach.called) != 0 {
		t.Errorf("a dead child was still attached to: %v", attach.called)
	}
}

// The epic id is accepted everywhere it was spelled as a run id: what the
// operator reads off status, events and the feed is epic-<id>, and typing
// that must drive the same run — without doubling the prefix.
func TestRunAcceptsTheRunIDSpellingOfTheEpicID(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	attach := &attachRecorder{}
	runAttach = attach.seam
	runHerdrLive = func(context.Context, string) (string, error) { return "", fmt.Errorf("none") }

	var stdout, stderr syncBuffer
	if code := runBody(context.Background(), t, []string{"--repo", repo, "epic-foo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if got := rec.argvs[0][1]; got != "foo" {
		t.Errorf("the run was asked to run epic %q, want foo — the prefix must not double", got)
	}
	if len(attach.called) != 1 || attach.called[0] != "epic-foo" {
		t.Errorf("the attach was asked for %v, want epic-foo — one run id, both spellings", attach.called)
	}
}

func TestRunNeedsExactlyOneEpicID(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"run", "a", "b"}, {"run", ""}, {"run", "epic-"}} {
		var stdout, stderr syncBuffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

// The end-to-end one: the production spawn, the production attach, real
// child processes, and the real signal. It is the only test that can prove
// what the criterion names — that the run this command starts survives the
// terminal that started it, that a second invocation attaches without
// starting anything, and that a stopped run is resumed — because a seam
// answers none of those.
//
// It costs the gate a few real process spawns and stays well under the
// discipline's own rules for load-bearing tests (the only subprocesses are
// the children under test and runlife's ps).
func TestRunStartsDetachedAttachesAndResumesForReal(t *testing.T) {
	// The production herdr probe runs too, and it must never reach the
	// operator's own herdr: a test started from a herdr pane inherits the
	// live server's socket in HERDR_SOCKET_PATH, and this test pinged it on
	// every run (and handed the child the herdr profile set because it
	// answered). A socket path nothing listens on is "no live herdr" — the
	// case this test is not about, answered the same on every host.
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "no-herdr.sock"))
	repo := t.TempDir()
	runID := "epic-det"
	startLog := filepath.Join(runlife.Dir(repo, runID), startLogName)

	// killRun stops a live child and waits for its death by the run's own
	// observable — the pidfile — never by a sleep. In cleanup it is
	// best-effort: a leak is caught by the wait, not by failing the cleanup.
	killRun := func(fail func(string, ...any)) {
		probe := runlife.Probe(repo, runID, time.Now())
		if probe.State != runlife.Alive {
			return
		}
		if err := syscall.Kill(probe.Record.PID, syscall.SIGTERM); err != nil {
			fail("could not stop the detached child pid %d: %v", probe.Record.PID, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if runlife.Probe(repo, runID, time.Now()).State != runlife.Alive {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		fail("the detached child outlived its SIGTERM")
	}
	t.Cleanup(func() { killRun(func(string, ...any) {}) })

	// oneRun is one `ticfac run det` invocation, ended the way a terminal
	// ends it: a real SIGINT to this process, which is exactly the signal a
	// Ctrl-C sends — and exactly the signal the detached child must not get.
	oneRun := func() (int, string) {
		var stdout, stderr syncBuffer
		code := make(chan int, 1)
		go func() { code <- Run([]string{"run", "--repo", repo, "det"}, &stdout, &stderr) }()
		// The attach's own proof: the run's feed line, printed by the live
		// view. Wait on the CONDITION, never on a guess about the timing.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(stdout.String(), "the detached child claimed this run") {
				break
			}
			select {
			case c := <-code:
				return c, stdout.String() + "\n--stderr--\n" + stderr.String()
			case <-time.After(10 * time.Millisecond):
			}
		}
		if !strings.Contains(stdout.String(), "the detached child claimed this run") {
			killRun(func(string, ...any) {})
			t.Fatalf("the attach never showed the run's own line:\n%s\n--stderr--\n%s",
				stdout.String(), stderr.String())
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			killRun(func(string, ...any) {})
			t.Fatalf("could not send the terminal's signal: %v", err)
		}
		select {
		case c := <-code:
			return c, stdout.String() + "\n--stderr--\n" + stderr.String()
		case <-time.After(10 * time.Second):
			killRun(func(string, ...any) {})
			t.Fatal("the attach never returned after Ctrl-C")
		}
		return 1, ""
	}

	// 1. A run that never ran is started in the background, and Ctrl-C
	//    detaches without stopping it — exiting the table's RUNNING class
	//    (5, tick 8v3): the epic is in flight, and an agent waiting on this
	//    command must not read "done" where the run keeps going.
	code, out := oneRun()
	if code != exitRunning {
		t.Fatalf("the first invocation exited %d, want %d (detached with the run live):\n%s", code, exitRunning, out)
	}
	for _, want := range []string{
		"starting it in the background",
		"detached from run epic-det",
		"keeps going in the background",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the first invocation does not say %q:\n%s", want, out)
		}
	}
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State != runlife.Alive {
		t.Fatalf("the run is %s after Ctrl-C, want alive — detaching must never stop it: %s",
			probe.State, probe.Reason)
	}
	if _, err := os.Stat(startLog); err != nil {
		t.Errorf("the started run's first words are not where the line says (%s): %v", startLog, err)
	}

	// 2. A second invocation on the LIVE run attaches and starts nothing:
	//    the tell is the starter's own log, which only a spawn writes.
	if err := os.Remove(startLog); err != nil {
		t.Fatal(err)
	}
	code, out = oneRun()
	if code != exitRunning {
		t.Fatalf("the second invocation exited %d, want %d (detached with the run live):\n%s", code, exitRunning, out)
	}
	if !strings.Contains(out, "run epic-det is alive") {
		t.Errorf("the second invocation does not say it is attaching to the live run:\n%s", out)
	}
	if _, err := os.Stat(startLog); err == nil {
		t.Error("the second invocation started a run: attaching must not spawn")
	}
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State != runlife.Alive {
		t.Fatalf("the run is %s after a second detach, want alive: %s", probe.State, probe.Reason)
	}

	// 3. A stopped run is resumed: the same command, the run's state, one
	//    new background incarnation.
	killRun(t.Fatalf)
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State == runlife.Alive {
		t.Fatalf("the run reads alive after its child was stopped: %s", probe.Reason)
	}
	code, out = oneRun()
	if code != exitRunning {
		t.Fatalf("the third invocation exited %d, want %d (detached with the resumed run live):\n%s", code, exitRunning, out)
	}
	if !strings.Contains(out, "resuming it in the background") {
		t.Errorf("the stopped run is not said to be resumed:\n%s", out)
	}
	if _, err := os.Stat(startLog); err != nil {
		t.Errorf("the resumed run's starter wrote no start.log: %v", err)
	}
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State != runlife.Alive {
		t.Fatalf("the resumed run is %s, want alive: %s", probe.State, probe.Reason)
	}
}

// equalArgv compares two argv slices as wholes, with a report that names both.
func equalArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// `run --json` answers once, at the command's end: what it did and how that
// ended, as the exit table's state words, with the run's own prose on
// stderr so stdout is the document's alone (tick 8v3). An attach that ends
// clean is the done/0 pair; the detach pair is pinned by the interrupted
// test above (running/5).
func TestRunJSONAnswersOnceWithProseOnStderr(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-json")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	attach := &attachRecorder{code: 0}
	runAttach = attach.seam
	herdrAnswers(t, "", fmt.Errorf("no herdr for the json test"))

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "--json", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout with --json is not one document:\n%s\n--stderr--\n%s", stdout.String(), stderr.String())
	}
	if doc["schema"] != "ticfac.run.v1" || doc["action"] != "attached" || doc["state"] != "done" {
		t.Errorf("the document is %v, want the attached/done answer", doc)
	}
	if doc["run_id"] != "epic-json" || doc["epic_id"] != "json" {
		t.Errorf("the document names the wrong run: %v", doc)
	}
	if !strings.Contains(stderr.String(), "attaching") {
		t.Errorf("the prose went to stdout with the document, not stderr:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "attaching") {
		t.Errorf("stdout carries prose beside the document:\n%s", stdout.String())
	}
}

// An attach that ends on a FAILED run answers failed/1, not done/0 (tick
// bot): `ticfac run` maps the watch's exit table word through
// runAttachState — the same mapping `run --cloud` uses — so an agent
// waiting on `ticfac run` reads the run's own failure in both the document's
// state word and the process code, without parsing the stream's prose.
func TestRunJSONAttachThatEndedFailedAnswersFailed(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-json")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	attach := &attachRecorder{code: exitGeneric}
	runAttach = attach.seam
	herdrAnswers(t, "", fmt.Errorf("no herdr for the json test"))

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "--json", "json"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit %d, want %d (the failed class) for an attach that ended on a failed run: %s%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout with --json is not one document:\n%s\n--stderr--\n%s", stdout.String(), stderr.String())
	}
	if doc["schema"] != "ticfac.run.v1" || doc["action"] != "attached" {
		t.Errorf("the document is %v, want the attached answer", doc)
	}
	if doc["state"] != agentStateFailed {
		t.Errorf("the document's state is %v, want %q — a failed run is not done", doc["state"], agentStateFailed)
	}
}

// An attach that ends on a CANCELLED run answers cancelled/7, not done/0
// (tick rix): `ticfac run` maps the watch's exit code through
// runAttachState, so an agent waiting on the attach reads the run's own
// deliberate stop in both the document's state word and the process code.
func TestRunJSONAttachThatEndedCancelledAnswersCancelled(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-json")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	attach := &attachRecorder{code: exitCancelled}
	runAttach = attach.seam
	herdrAnswers(t, "", fmt.Errorf("no herdr for the json test"))

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "--json", "json"}, &stdout, &stderr)
	if code != exitCancelled {
		t.Fatalf("exit %d, want %d (the cancelled class) for an attach that ended on a cancelled run: %s%s", code, exitCancelled, stdout.String(), stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout with --json is not one document:\n%s\n--stderr--\n%s", stdout.String(), stderr.String())
	}
	if doc["schema"] != "ticfac.run.v1" || doc["action"] != "attached" {
		t.Errorf("the document is %v, want the attached answer", doc)
	}
	if doc["state"] != agentStateCancelled {
		t.Errorf("the document's state is %v, want %q — a cancelled run is not done", doc["state"], agentStateCancelled)
	}
}
