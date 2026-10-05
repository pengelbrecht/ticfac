package cli

// Tick mwt: the bare overview's rows around the live epic run were wrong
// for every run whose id carries no epic — the factory's run_<hex>, resumed
// or left behind locally and registered on this machine. Their records are
// not found from a checkout that never hosted them (the id names no branch
// to fetch), so the row's model carried epic_id "" and everything the epic
// should have carried was lost: the superseded marking (a row with an
// empty epic never enters markOverviewHistory's `latest`), and the clearing
// commands' operand ("clear with: ticfac run-epic " with nothing after it,
// "ticfac settle  yjq 12 --run-id …" with the epic's space left empty).
// The run's own checkpoint — on origin's epic branch, the one durable place
// that names both ids — is what resolves the epic; and where even that
// cannot be found, no command is printed at all rather than one a person
// cannot run. The row's first line is bounded to the width the screen
// names, the reason the only part it may cut.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// mwtEarlierRunID is the earlier run of epic mwx under the factory's own
// spelling — run_ plus 32 hex, no epic in it — the id a local resume of a
// cloud run registers on this machine.
const mwtEarlierRunID = "run_09ebaf290f934172912b00bd112d8e06"

// mwtHeldRunID and mwtGoneRunID are two runs whose id names no epic AND
// whose records exist nowhere this machine can read: the first left a hold
// on its feed before dying, the second just died. Neither may name a
// clearing command: the commands' epic operand is the one thing their
// unresolvable models cannot state.
const (
	mwtHeldRunID = "run_3f034e683faf4fdf9f6c0952fc158524"
	mwtGoneRunID = "run_ee8ebb4fe13d4311b8f9be6f66db69d4"
)

// mwtEpic is the epic both fixtures' runs belong to.
const mwtEpic = "mwx"

// mwtRegisteredRunFixture builds the machine the tick names: a bare origin,
// the working checkout the runs worked in, and the operator's checkout the
// bare `ticfac` is glanced at from — a checkout that holds none of the runs'
// state in its working tree. The earlier run's durable records are on
// origin's epic/mwx branch and nowhere else; the live run works in the
// working checkout, so the registry is the only thing that names where
// either is read.
func mwtRegisteredRunFixture(t *testing.T, now time.Time) (working, operator string, release func()) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	working = filepath.Join(root, "working")
	operator = filepath.Join(root, "operator")
	git := func(dir string, args ...string) {
		t.Helper()
		execTestCmd(t, dir, "git", args...)
	}
	git(root, "init", "--quiet", "--bare", "-b", "main", bare)
	git(root, "init", "--quiet", "-b", "epic/"+mwtEpic, seed)
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "-A")
	git(seed, "-c", "user.email=mwt@example.com", "-c", "user.name=mwt test",
		"commit", "--quiet", "-m", "seed")
	git(seed, "branch", "main")
	git(seed, "push", "--quiet", bare, "epic/"+mwtEpic, "main")
	git(root, "clone", "--quiet", bare, working)
	git(root, "clone", "--quiet", bare, operator)

	// The earlier run's durable records, on the epic branch its own
	// checkpoint names — committed by plumbing, never in a working tree.
	store, err := runstate.Open(runstate.Options{
		Repo: working, Remote: "origin", Branch: "epic/" + mwtEpic, RunID: mwtEarlierRunID,
	})
	if err != nil {
		t.Fatalf("open the earlier run's state store: %v", err)
	}
	one, executor := 1, "local-subprocess"
	if outcome, err := store.PutCheckpoint(runstate.Checkpoint{
		RunID:  mwtEarlierRunID,
		EpicID: mwtEpic,
		State:  runstate.StateFailed,
		Reason: "the integrated gate refused attempt 3 of t2: go test failed",
		Ticks:  []runstate.TickState{{TickID: "t2", State: "rejected", Attempt: one}},
		Provenance: runstate.Provenance{
			RunID:     mwtEarlierRunID,
			SourceRef: "refs/heads/epic/" + mwtEpic,
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			Phase:     runstate.PhaseWorker,
			Executor:  &executor,
		},
	}); err != nil || !outcome.EffectPermitted() {
		t.Fatalf("put the earlier run's checkpoint on epic/%s: outcome %v, err %v", mwtEpic, outcome, err)
	}

	// The earlier run's feed, in the checkout it worked in — exhaust, never
	// pushed. A failed ending, holding nothing.
	feed := runfeed.Open(working, mwtEarlierRunID)
	if err := feed.Append(runfeed.NewEvent(now.Add(-80*time.Minute), mwtEarlierRunID, "t2", &one,
		reconcile.StageDispatched, "t2 try 1 dispatched")); err != nil {
		t.Fatalf("append the earlier run's dispatched line: %v", err)
	}
	if err := feed.Append(runfeed.NewEvent(now.Add(-70*time.Minute), mwtEarlierRunID, "", nil,
		reconcile.StageRunFinished, "failed: the integrated gate refused attempt 3 of t2: go test failed")); err != nil {
		t.Fatalf("append the earlier run's failed ending: %v", err)
	}

	// The earlier run's registration, backdated: it ran before the live run
	// of the same epic, which is what makes the live run the epic's answer.
	mwtRegister(t, mwtEarlierRunID, working, now.Add(-2*time.Hour))

	// The live run of the same epic, claimed in the working checkout: it
	// registers itself at now, the later start that supersedes the earlier
	// run's stop.
	life, err := runlife.Claim(working, "epic-"+mwtEpic)
	if err != nil {
		t.Fatalf("claim the live run in its working checkout: %v", err)
	}
	release = func() { life.Release("mwt test") }

	// The fixture's own honesty: neither checkout holds a run directory in
	// its working tree — the earlier run's records are on the branch only.
	for _, checkout := range []string{working, operator} {
		if _, err := os.Stat(filepath.Join(checkout, runstate.Root, "runs")); err == nil {
			t.Fatalf("%s holds a .ticfac/runs directory in its working tree: the fixture must hold the run's state only on epic/%s", checkout, mwtEpic)
		}
	}
	return working, operator, release
}

// mwtRegister writes one run's machine registration with the timestamp the
// caller states: a dead run's registration stays by design (only a
// checkout's deletion removes it), and its start is what orders the runs of
// one epic — a fact the fixture must control.
func mwtRegister(t *testing.T, runID, repo string, at time.Time) {
	t.Helper()
	host, _ := os.Hostname()
	raw := fmt.Sprintf(`{"schema_version":1,"run_id":%q,"repo":%q,"host":%q,"registered_at":%q}`,
		runID, repo, host, at.UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(runregistry.Dir(), runID+".json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("register %s: %v", runID, err)
	}
}

// mwtUnresolvableRunFixture builds the runs the resolution cannot answer for:
// registered on this machine, their ids name no epic, and no branch anywhere
// carries their checkpoints — a run that died before any record was pushed.
// One left a hold on its feed; the other left nothing at all.
func mwtUnresolvableRunFixture(t *testing.T, now time.Time) (working string) {
	t.Helper()
	working = t.TempDir()
	execTestCmd(t, working, "git", "init", "--quiet", "-b", "main", working)
	execTestCmd(t, working, "git", "config", "user.email", "mwt@example.com")
	execTestCmd(t, working, "git", "config", "user.name", "mwt test")
	if err := os.WriteFile(filepath.Join(working, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execTestCmd(t, working, "git", "add", "-A")
	execTestCmd(t, working, "git", "commit", "--quiet", "-m", "base")

	two := 2
	feed := runfeed.Open(working, mwtHeldRunID)
	if err := feed.Append(runfeed.NewEvent(now.Add(-40*time.Minute), mwtHeldRunID, "yjq", &two,
		reconcile.StageRunHeld, "attempt_struck_out: the report names no status")); err != nil {
		t.Fatalf("append the held run's hold line: %v", err)
	}
	for _, runID := range []string{mwtHeldRunID, mwtGoneRunID} {
		mwtRegister(t, runID, working, now.Add(-30*time.Minute))
	}
	return working
}

// TestTheOverviewResolvesTheEpicOfARegisteredRun: a run id that carries no
// epic still belongs to one — its checkpoint names it — and the listing
// finds the checkpoint where the run's own plumbing put it, on origin's
// epic branches. The row resolves its epic, the later run of the same epic
// supersedes its stop (the marking an empty epic could never reach), and
// the clearing command carries the epic a person would type.
func TestTheOverviewResolvesTheEpicOfARegisteredRun(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir()) // no factory: the local half is the question
	ownRegistry(t)
	fakeOverviewGraph(t)
	fakeEpicStatus(t)
	_, operator, release := mwtRegisteredRunFixture(t, now)
	defer release()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", operator, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	rows := map[string]overviewRun{}
	for _, row := range doc.Runs {
		rows[row.RunID] = row
	}
	earlier, ok := rows[mwtEarlierRunID]
	if !ok {
		t.Fatalf("the registered run is not listed:\n%s", stdout.String())
	}
	if earlier.EpicID != mwtEpic {
		t.Errorf("the registered run's row carries epic %q, want %q from its checkpoint on the epic branch", earlier.EpicID, mwtEpic)
	}
	if !earlier.History || !strings.Contains(earlier.HistoryReason, "superseded by epic-"+mwtEpic) {
		t.Errorf("the registered run is not marked superseded by the later run of its epic: history=%v, reason=%q",
			earlier.History, earlier.HistoryReason)
	}
	if earlier.ClearWith == nil || *earlier.ClearWith != "ticfac run-epic "+mwtEpic {
		t.Errorf("the registered run's resume is %v, want the epic's own run-epic command", earlier.ClearWith)
	}
	live, ok := rows["epic-"+mwtEpic]
	if !ok || live.State != overviewStateRunning || live.History {
		t.Errorf("the live run of the epic reads %+v — it is the epic's answer, never history", live)
	}

	// The human view: the superseded run is collapsed; the live one shows.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", operator}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if line := lineOf(out, mwtEarlierRunID); line != "" {
		t.Errorf("the superseded run still has a line of its own:\n%s", out)
	}
	if !strings.Contains(out, "1 older run not shown") {
		t.Errorf("the collapsed history is not the one summary line:\n%s", out)
	}

	// --all lists it, saying why it is history.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", operator, "--all"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --all exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if line := lineOf(stdout.String(), mwtEarlierRunID); !strings.Contains(line, "superseded by epic-"+mwtEpic) {
		t.Errorf("the --all line does not name the later run that answers it: %q", line)
	}

	// The one-shot surface answers the same: `ticfac status <run-id> --json`
	// for the unepic id resolves the epic from the same checkpoint. The exit
	// code stays liveness's alone — the run is dead, so 1 — but the answer
	// it prints carries the epic.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--repo", operator, "--json", mwtEarlierRunID}, &stdout, &stderr); code != 1 {
		t.Fatalf("status of the registered run exits %d, want the dead run's own 1:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(stdout.Bytes(), &model); err != nil {
		t.Fatalf("the status JSON does not decode: %v\n%s", err, stdout.String())
	}
	if model.EpicID != mwtEpic {
		t.Errorf("the status model of the registered run carries epic %q, want %q", model.EpicID, mwtEpic)
	}
}

// mwtVerb finds every printed ticfac command by its verb, longest first so
// run-epic is never read as run.
var mwtVerb = regexp.MustCompile(`ticfac (run-epic|settle|triage|run)`)

// mwtRefuseAnOperandlessCommand fails the test when any ticfac command the
// surface printed has lost an operand: the verb followed by the line's end,
// by the row's own " — " separator, or by a second space where the epic
// should be — and a flag that names no value. A command a person cannot run
// is worse than no command, and the surfaces must never print one.
func mwtRefuseAnOperandlessCommand(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		for _, at := range mwtVerb.FindAllStringSubmatchIndex(line, -1) {
			after := line[at[1]:]
			rest := strings.TrimLeft(after, " ")
			if rest == "" || strings.HasPrefix(rest, "—") || strings.HasPrefix(after, "  ") {
				t.Errorf("a printed ticfac command is missing its operand: %q", line)
			}
		}
		if strings.HasSuffix(strings.TrimRight(line, " "), "--run-id") {
			t.Errorf("a printed --run-id names no run: %q", line)
		}
	}
}

// mwtCarriesItsOperands is whether a command string keeps every operand: no
// trailing space, no double space where one should be.
func mwtCarriesItsOperands(command string) bool {
	return command != "" && !strings.HasSuffix(command, " ") && !strings.Contains(command, "  ")
}

// TestTheOverviewPrintsNoCommandWithAnEmptyOperand: a run whose epic nothing
// can resolve — no branch carries its checkpoint — must name no clearing
// command at all, in prose and in JSON: the hold still shows, the stop still
// shows, but "ticfac run-epic " with nothing after the verb and "ticfac
// settle  yjq 12" with the epic's space left empty are commands a person
// cannot run, printed as if they could.
func TestTheOverviewPrintsNoCommandWithAnEmptyOperand(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir())
	ownRegistry(t)
	fakeOverviewGraph(t)
	fakeEpicStatus(t)
	working := mwtUnresolvableRunFixture(t, now)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", working, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	rows := map[string]overviewRun{}
	for _, row := range doc.Runs {
		rows[row.RunID] = row
	}
	for _, runID := range []string{mwtHeldRunID, mwtGoneRunID} {
		row, ok := rows[runID]
		if !ok {
			t.Fatalf("%s is not listed:\n%s", runID, stdout.String())
		}
		if row.State != overviewStateHeld {
			t.Errorf("%s reads %q, want held for a person: the stop itself still shows\n%s", runID, row.State, stdout.String())
		}
		if row.ClearWith != nil {
			t.Errorf("%s names the clearing command %q with no epic to put in it", runID, *row.ClearWith)
		}
		for _, attention := range row.Model.Attention {
			if attention.UnblockCommand != nil && !mwtCarriesItsOperands(*attention.UnblockCommand) {
				t.Errorf("%s's attention carries the operandless command %q", runID, *attention.UnblockCommand)
			}
		}
		if row.Model.WaitsOn != nil && row.Model.WaitsOn.UnblockCommand != nil &&
			!mwtCarriesItsOperands(*row.Model.WaitsOn.UnblockCommand) {
			t.Errorf("%s's wait carries the operandless command %q", runID, *row.Model.WaitsOn.UnblockCommand)
		}
	}

	// The prose view says the same, and no line anywhere names a command
	// that lost an operand.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", working}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	mwtRefuseAnOperandlessCommand(t, out)
	for _, runID := range []string{mwtHeldRunID, mwtGoneRunID} {
		if line := lineOf(out, runID); line == "" {
			t.Errorf("%s is not listed:\n%s", runID, out)
		} else if strings.Contains(line, "clear with:") {
			t.Errorf("%s names a clearing command its unresolvable epic cannot spell: %q", runID, line)
		}
	}
}

// TestTheOverviewBoundsEveryRowToTheWidth: the row's first line is a glance,
// not a transcript — a run's own last word can run to thousands of
// characters, and unbounded it pushes everything else off the screen. Every
// line the overview prints fits the width it lays out at; the reason is the
// one part the bound may cut, and the clearing command — the part a person
// types — survives it whole, wrapped under the row when the line cannot
// seat it.
func TestTheOverviewBoundsEveryRowToTheWidth(t *testing.T) {
	command := "ticfac run-epic " + mwtEpic
	longReason := strings.Repeat("the integrated gate refused attempt 3 of t2: go test failed because the short suite is long; ", 20)
	doc := overviewModel{Runs: []overviewRun{{
		RunID: mwtEarlierRunID,
		Host:  statusmodel.HostLocal,
		State: overviewStateFailed,
		Reason: "failed: " + longReason,
		ClearWith: func() *string {
			c := command
			return &c
		}(),
		// provisional: the row is the question here, and no dashboard
		// headline rides under it.
		provisional: true,
	}}}

	check := func(t *testing.T, out string, width int) {
		t.Helper()
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("a printed line is %d cells wide, over the %d the screen names:\n%q", w, width, line)
			}
		}
		// The command survives the bound, word for word, on some line of
		// its own — never cut.
		for _, word := range strings.Fields(command) {
			if !strings.Contains(out, word) {
				t.Errorf("the bound cut the clearing command short: %q is gone from\n%s", word, out)
			}
		}
	}

	var buf bytes.Buffer
	renderOverview(&buf, doc, "", false)
	check(t, buf.String(), overviewHeadlineFallbackWidth)

	// A terminal's own width — 120 columns, the width the defect showed
	// at, spoken by the terminal's own seams.
	realTTY, realSize := watchIsTerminal, watchTerminalSize
	t.Cleanup(func() { watchIsTerminal, watchTerminalSize = realTTY, realSize })
	watchIsTerminal = func(io.Writer) bool { return true }
	watchTerminalSize = func(io.Writer) (int, int, bool) { return 120, 24, true }
	buf.Reset()
	renderOverview(&buf, doc, "", false)
	check(t, buf.String(), 120)
}
