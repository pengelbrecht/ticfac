package cli

// The agent surface's property tests (tick z7w): the --json contract and the
// exit table, held as PROPERTIES over generated run states and argument
// sequences — the inputs a fixture writer never thinks to draw — with Hegel
// (hegel-go) the way internal/runconfig, internal/forge, internal/runstate,
// internal/exec/subprocess and internal/reconcile already use it.
//
// The subject is the CLI's own answer: one invocation of Run, in process,
// against a world the generator builds — a run's feed as a real run leaves
// one (dispatched lines, gate failures, holds, resumes, the terminal word),
// or no feed at all (a lookup that honestly comes back empty). The world is
// stamped against one fixed base time, so no property reads a wall clock and
// every failure reproduces from its drawn timeline.
//
// The properties are the ones tick z7w names for this half:
//
//	P1  ONE DOCUMENT, ITS OWN VERSION: every --json command answers with
//	    exactly one document on stdout, naming the versioned schema its
//	    command documents (ticfac.<command>.v1, the status model's own id,
//	    the overview's schema_version) — or with an empty stdout when the
//	    command refuses. Never two documents, never prose, never silence on
//	    a success.
//	P2  THE EXIT CODE IS THE TABLE'S, AND AGREES WITH THE STATE WORD: the
//	    code is one ExitTable names, and where the document carries a state
//	    word the code is that word's class — the agreement 8v3 built the
//	    whole surface on. `status --json` keeps its own documented
//	    exception (the code is liveness's alone: 0 alive, 1 not), and the
//	    property holds it to exactly that instead.
//	P3  STDOUT IS THE DOCUMENT'S ALONE, STDERR IS PROSE'S ALONE: whatever a
//	    --json invocation writes, stdout is either empty or exactly one
//	    parseable document, and stderr NEVER carries the document — no
//	    parseable JSON object on stderr, the document's schema word never
//	    there. Prose the command still wants a human to read (a refusal's
//	    remedy, a hold's clearing command) is stderr's by design
//	    (agentjson.go); the half an agent's parse depends on is that the
//	    document never leaks.
//	P4  FLAGS PARSE IN ANY ORDER (#75's remedy rule): one invocation's flag
//	    groups — a flag with its value kept whole — and its positional,
//	    permuted every way, answer identically: same exit code, same
//	    stdout. The remedy rule exists because Go's flag package stopped at
//	    the first positional and a command the run PRINTS for a person was
//	    refused by the CLI it named.
//	P5  A HOLD THE RUN'S ANSWER DOES NOT CARRY NEVER ALERTS: the watch's
//	    hold alert (its whole reason to exist — 0z0) fires exactly when the
//	    run's own answer is held: an unanswered run_held line gets the
//	    alert and its clearing command; a hold a RESUME answered is that
//	    incarnation's history, never the current one's alarm. The stale
//	    hold showing as current is the same defect class as the phantom
//	    "running" cloud run the tick names, seen from the hold's side —
//	    and P5 is the property that caught it, on the tree as it stood
//	    (a resumed, completed run's watch still alerted its old hold).
//	P6  A GROWING FEED NEVER LIES: after every prefix of a generated
//	    timeline, events --json answers with exactly the events that stand
//	    — same count, same order, same stages — one document each time.
//	    The stateful half: the feed is a run's state machine, and every
//	    intermediate state owes the same answer as the final one.
//
// Non-vacuity is proven in the gate, not argued:
// TestAgentJSONPropertiesCatchSeededBugs runs each property against a
// deliberately broken CLI answer — the document without its schema, a code
// off the table, the document leaked to stderr, a parse that drops flag
// groups after the first positional (#75's exact bug), an alert on every
// watch, a feed that loses its last event — and requires the property to
// fail for at least one generated world.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// ---------------------------------------------------------------------------
// The generator: one run world, and the argv that asks about it
// ---------------------------------------------------------------------------

// pbtBase is every generated world's own `now`: the stamps the generator
// writes are measured back from it, so no property reads a wall clock and no
// age drifts between runs.
var pbtBase = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// pbtStep is one line of a generated timeline.
type pbtStep struct {
	stage  string
	detail string
	// tick and attempt carry the line's identity; attempt 0 means none.
	tick, attempt int
}

// pbtWorld is one run the generator built: its id, the timeline a real run
// left in its feed, and the answer the documented contract owes.
type pbtWorld struct {
	runID string
	steps []pbtStep
	// state is the state word the watch's own contract says this timeline
	// ends by: done, failed, cancelled, held — or "" for an open feed (no
	// terminal line stands; only the one-shot commands ask about it, never
	// the watch, which would follow it) or a missing run (no feed at all).
	state string
	// missing: no feed exists — a lookup that honestly comes back empty.
	missing bool
	// answeredHold says a resume stands after the last run_held line: the
	// hold is history, and P5 expects no alert for it.
	answeredHold bool
	// garbage, when true, appends a line that is not a run event at all:
	// the reject-direction world, whose answer must be a refusal, never a
	// document built out of noise.
	garbage bool
}

// pbtHoldDetails are the hold kinds the reconciler's own closed set writes
// into run_held lines: the generator draws from the vocabulary the run
// really uses, so a kind added to the set is exercised the moment it lands.
var pbtHoldDetails = []string{
	reconcile.RefusedFindingUntriaged, reconcile.RefusedUnaddressed,
	reconcile.RefusedNeedsHuman, reconcile.RefusedCollect, reconcile.RefusedGate,
}

// pbtDeathDetails are the deaths a run_died line really carries — drawn from
// the words real deaths write, none of them the cancelled word, because a
// death's class is failed unless its own detail says the state-led
// "cancelled:" and the oracle says exactly that.
var pbtDeathDetails = []string{
	"stopped by an eviction of the container",
	"the reconciler panicked on a malformed record",
	"an operational error reading the store",
	"the harness socket closed before the collect",
}

// pbtTerminalKind is the ending a drawn timeline takes.
type pbtTerminalKind int

const (
	pbtEndNone pbtTerminalKind = iota
	pbtEndDone
	pbtEndFailed
	pbtEndCancelled
	pbtEndDied
	pbtEndHeld
)

func (k pbtTerminalKind) String() string {
	switch k {
	case pbtEndDone:
		return "done"
	case pbtEndFailed:
		return "failed"
	case pbtEndCancelled:
		return "cancelled"
	case pbtEndDied:
		return "died"
	case pbtEndHeld:
		return "held"
	}
	return "open"
}

// pbtProse draws detail text that cannot carry a class word: the classes are
// read from the TERMINAL line's own prefix, so the tails are drawn from an
// alphabet the classifier reads nothing into — the property is about the
// structure, never about prose that would forge a marker.
func pbtProse(tc hegel.TestCase) string {
	return hegel.Draw(tc, hegel.Text().MinSize(4).MaxSize(40).Alphabet(
		"abcdefghijklmnopqrstuvwxyz "))
}

// genWorld draws one run world: an id shape, a timeline whose stamps ascend
// from the base time, and an ending from the shapes real runs write — an
// open feed, a clean end, a failed end, a deliberate stop, a death, a hold
// that stands unanswered, or the same hold answered by a resume. mustEnd
// excludes the open shape: the watch would follow an open feed, so a subject
// that watches draws worlds that ended.
func genWorld(tc hegel.TestCase, mustEnd, refusals bool) pbtWorld {
	w := pbtWorld{}
	// The id shape a real run id takes: the epic spelling or a bare run id.
	if hegel.Draw(tc, hegel.Booleans()) {
		w.runID = "epic-" + hegel.Draw(tc, hegel.Text().MinSize(3).MaxSize(3).Alphabet(
			"abcdefghijklmnopqrstuvwxyz"))
	} else {
		w.runID = "run-" + hegel.Draw(tc, hegel.Text().MinSize(3).MaxSize(3).Alphabet(
			"abcdefghijklmnopqrstuvwxyz"))
	}
	if refusals && hegel.Draw(tc, hegel.WeightedBooleans(0.15)) {
		// A lookup that honestly comes back empty: no feed at all.
		w.missing = true
		return w
	}
	middle := func() pbtStep {
		stage := hegel.Draw(tc, hegel.SampledFrom([]string{
			reconcile.StageGatePassed, reconcile.StageGateFailed,
			reconcile.StageStallWarned, reconcile.StageRejected, reconcile.StageIntegrated,
		}))
		return pbtStep{stage: stage,
			detail:  pbtProse(tc),
			tick:    hegel.Draw(tc, hegel.Integers(1, 4)),
			attempt: hegel.Draw(tc, hegel.Integers(1, 3)),
		}
	}
	for i := 0; i < hegel.Draw(tc, hegel.Integers(1, 4)); i++ {
		w.steps = append(w.steps, pbtStep{
			stage:   reconcile.StageDispatched,
			detail:  pbtProse(tc),
			tick:    hegel.Draw(tc, hegel.Integers(1, 4)),
			attempt: hegel.Draw(tc, hegel.Integers(1, 3)),
		})
	}
	for i := 0; i < hegel.Draw(tc, hegel.Integers(0, 3)); i++ {
		w.steps = append(w.steps, middle())
	}
	// The ending: a hold (answered or not), a clean end, a failure, a stop
	// or a death — the vocabulary the exit table separates.
	kinds := []pbtTerminalKind{
		pbtEndNone, pbtEndDone, pbtEndFailed, pbtEndCancelled, pbtEndDied, pbtEndHeld,
	}
	if mustEnd {
		kinds = kinds[1:]
	}
	kind := hegel.Draw(tc, hegel.SampledFrom(kinds))
	hold := func() pbtStep {
		return pbtStep{
			stage:   reconcile.StageRunHeld,
			detail:  hegel.Draw(tc, hegel.SampledFrom(pbtHoldDetails)) + ": " + pbtProse(tc),
			tick:    hegel.Draw(tc, hegel.Integers(1, 4)),
			attempt: hegel.Draw(tc, hegel.Integers(1, 3)),
		}
	}
	terminal := func(stage, detail string) {
		w.steps = append(w.steps, pbtStep{stage: stage, detail: detail})
	}
	switch kind {
	case pbtEndHeld:
		w.steps = append(w.steps, hold())
		if hegel.Draw(tc, hegel.WeightedBooleans(0.5)) {
			// The hold a resume answered: the run moved on, so the hold is
			// history — and the run's own answer is the word its LAST
			// terminal line carries, never the old hold.
			w.answeredHold = true
			w.steps = append(w.steps, pbtStep{stage: runfeed.StageResumed, detail: pbtProse(tc)})
			if hegel.Draw(tc, hegel.Booleans()) {
				terminal(reconcile.StageRunFinished, "completed")
				w.state = agentStateDone
			} else {
				terminal(reconcile.StageRunFinished, "failed: "+pbtProse(tc))
				w.state = agentStateFailed
			}
		} else {
			// The hold that stands: the run's answer is held, whatever its
			// terminal word was.
			w.state = agentStateHeld
			if hegel.Draw(tc, hegel.Booleans()) {
				terminal(reconcile.StageRunFinished, "failed: "+pbtProse(tc))
			} else {
				terminal(reconcile.StageRunFinished, "cancelled: "+pbtProse(tc))
			}
		}
	case pbtEndDone:
		terminal(reconcile.StageRunFinished, "completed")
		w.state = agentStateDone
	case pbtEndFailed:
		terminal(reconcile.StageRunFinished, "failed: "+pbtProse(tc))
		w.state = agentStateFailed
	case pbtEndCancelled:
		terminal(reconcile.StageRunFinished, "cancelled: "+pbtProse(tc))
		w.state = agentStateCancelled
	case pbtEndDied:
		terminal(runfeed.StageRunDied, hegel.Draw(tc, hegel.SampledFrom(pbtDeathDetails)))
		w.state = agentStateFailed
	case pbtEndNone:
		// An open feed: no terminal line stands. The one-shot commands
		// answer it; the watch would follow it, so the watch's properties
		// draw mustEnd worlds.
	}
	if refusals && hegel.Draw(tc, hegel.WeightedBooleans(0.1)) {
		// The reject-direction world: a feed whose last line is not a run
		// event at all. The answer must be a refusal — prose on stderr, an
		// empty stdout, a code from the table — never a document built out
		// of noise.
		w.garbage = true
		w.state = ""
	}
	return w
}

// pbtWrite builds the world on disk: the feed under the repo's .ticfac/logs,
// one line per step. A missing world writes nothing; a garbage world's last
// line is bytes that are not an event.
func pbtWrite(tb testing.TB, w pbtWorld) (repo string) {
	tb.Helper()
	repo = tb.TempDir()
	if w.missing {
		return repo
	}
	dir := filepath.Join(repo, runstate.Root, "logs", w.runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("create the feed directory: %v", err)
	}
	feed := runfeed.Open(repo, w.runID)
	for i, step := range w.steps {
		// A run-level line carries no tick and no attempt — nil, never the
		// zero values: the feed's attempt is 1-based and a nil is the line's
		// own word for "the run, not an attempt".
		tickID := ""
		var attempt *int
		if step.tick > 0 {
			tickID = fmt.Sprintf("t%d", step.tick)
		}
		if step.attempt > 0 {
			attempt = &step.attempt
		}
		event := runfeed.NewEvent(pbtBase.Add(time.Duration(i+1)*time.Minute),
			w.runID, tickID, attempt, step.stage, step.detail)
		if err := feed.Append(event); err != nil {
			tb.Fatalf("append %s to the feed: %v", step.stage, err)
		}
	}
	if w.garbage {
		// A line that is not a run event: the parser must refuse the feed,
		// and no property may read anything out of it.
		handle, err := os.OpenFile(runfeed.Path(repo, w.runID), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			tb.Fatalf("open the feed: %v", err)
		}
		if _, err := handle.WriteString("this is not a run event feed line at all\n"); err != nil {
			tb.Fatalf("append the noise line: %v", err)
		}
		if err := handle.Close(); err != nil {
			tb.Fatalf("close the feed: %v", err)
		}
	}
	return repo
}

// pbtWorldString is the failure message's world: the whole timeline, so a
// counterexample reproduces by hand.
func pbtWorldString(w pbtWorld) string {
	var lines []string
	for i, step := range w.steps {
		who := fmt.Sprintf("t%d#%d", step.tick, step.attempt)
		lines = append(lines, fmt.Sprintf("  %d. %s %s: %s", i+1, who, step.stage, step.detail))
	}
	state := w.state
	if state == "" {
		state = "(open)"
	}
	return fmt.Sprintf("run %s (state %s, answeredHold %v, missing %v, garbage %v):\n%s",
		w.runID, state, w.answeredHold, w.missing, w.garbage, strings.Join(lines, "\n"))
}

// ---------------------------------------------------------------------------
// The runner: one argv, in process, and the answer a property reads
// ---------------------------------------------------------------------------

// pbtAnswer is one invocation's whole answer.
type pbtAnswer struct {
	code   int
	stdout string
	stderr string
}

// pbtRunner runs one argv. It is a type so the seeded-bug harness can wrap
// it: a broken CLI is a broken runner, and every property checks the same
// thing through it.
type pbtRunner func(argv []string) pbtAnswer

// pbtInProcess is the real CLI: Run, in process, through buffers.
func pbtInProcess(argv []string) pbtAnswer {
	var stdout, stderr bytes.Buffer
	code := Run(argv, &stdout, &stderr)
	return pbtAnswer{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// pbtDecodeOne parses exactly one document: one JSON value, nothing after
// it. Anything else — a second document, prose, half a document — is the
// failure the property exists to catch.
func pbtDecodeOne(stdout string) (map[string]any, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, fmt.Errorf("no document")
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("stdout carries more than one document")
	}
	return doc, nil
}

// pbtHegelDoc is pbtDecodeOne for a hegel case, reported through the
// property's own failure channel.
func pbtHegelDoc(ht *hegel.T, ans pbtAnswer) map[string]any {
	doc, err := pbtDecodeOne(ans.stdout)
	if err != nil {
		ht.Fatalf("%v (exit %d):\n%s", err, ans.code, ans.stdout)
	}
	return doc
}

// pbtStderrIsProse answers whether stderr carries no document: no JSON
// object with a schema field parses out of it. The half an agent's parse
// depends on: the document lives on stdout alone.
func pbtStderrIsProse(stderr string) bool {
	if !strings.Contains(stderr, "{") {
		return true
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil {
		return true // prose that happens to brace is still prose
	}
	_, carriesSchema := doc["schema"]
	return !carriesSchema
}

// pbtExitTableCodes is the documented table's codes, the closed set a
// command may exit with.
func pbtExitTableCodes() map[int]bool {
	codes := map[int]bool{}
	for _, entry := range ExitTable {
		codes[entry.Code] = true
	}
	return codes
}

// pbtRunCommands is the three run surfaces the generator drives in process
// without an environment: events, status and watch. The overview and the
// cloud family need the world's environment redirected, so the overview has
// its own property below rather than a drawn command here.
var pbtRunCommands = []string{"events", "status", "watch"}

// ---------------------------------------------------------------------------
// P1 — one document, its own version
// ---------------------------------------------------------------------------

// short: the --json answer over generated run states, in process, no git, no processes
func TestPBTJSONAnswersOneDocumentNamingItsSchema(t *testing.T) {
	t.Parallel()
	table := pbtExitTableCodes()
	hegel.Test(t, func(ht *hegel.T) {
		command := hegel.Draw(ht, hegel.SampledFrom(pbtRunCommands))
		w := genWorld(ht, command == "watch", true)
		repo := pbtWrite(ht, w)
		ans := pbtInProcess([]string{command, "--json", "--repo", repo, w.runID})
		// A missing run, or a feed that cannot be parsed, refuses on the
		// feed-reading surfaces: the honest answer is prose on stderr and an
		// empty stdout, and the property's refusal half is exactly that — no
		// document, a code the table names. `status` is the exception by
		// design: it answers liveness over a feed it cannot parse with a
		// DEGRADED model document (the unreadable source named in the
		// document's own degraded list), and its own property below holds
		// it to that answer instead.
		if w.missing || (w.garbage && command != "status") {
			if strings.TrimSpace(ans.stdout) != "" {
				ht.Fatalf("a refused %s wrote a document anyway (exit %d):\n%s\n%s",
					command, ans.code, ans.stdout, pbtWorldString(w))
			}
			if !table[ans.code] {
				ht.Fatalf("a refused %s exited %d, a code the table does not name\n%s",
					command, ans.code, pbtWorldString(w))
			}
			return
		}
		doc := pbtHegelDoc(ht, ans)
		var schema any
		switch command {
		case "watch":
			schema = doc["schema"]
		case "events":
			schema = doc["schema"]
		case "status":
			schema = doc["schema_version"]
		}
		if schema == nil || schema == "" {
			ht.Fatalf("the %s document names no versioned schema:\n%v\n%s",
				command, doc, pbtWorldString(w))
		}
		switch command {
		case "watch":
			if schema != agentSchemaID("watch") {
				ht.Fatalf("the watch document's schema is %v, want %q\n%s",
					schema, agentSchemaID("watch"), pbtWorldString(w))
			}
		case "events":
			if schema != agentSchemaID("events") {
				ht.Fatalf("the events document's schema is %v, want %q\n%s",
					schema, agentSchemaID("events"), pbtWorldString(w))
			}
		case "status":
			if schema != float64(statusmodel.SchemaVersion) {
				ht.Fatalf("the status document's schema version is %v, want %d\n%s",
					schema, statusmodel.SchemaVersion, pbtWorldString(w))
			}
		}
	}, hegel.WithTestCases(60))
}

// The overview document: one versioned answer whose rows are exactly the
// runs the world holds — nothing vanishes, history included, because --json
// stays complete by design. Serial, not parallel: the property holds the
// environment (a HOME with no factory in it) and the tracker seams for the
// whole test, the same way the overview's own tests do.
func TestPBTOverviewJSONListsEveryRunTheWorldHolds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	realGraph, realCI, realCost := epicGraph, statusCI, statusWorkerCost
	epicGraph = func(context.Context, string, string) *tk.Graph { return nil }
	statusCI = func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil }
	statusWorkerCost = func(context.Context, string) (*statusmodel.WorkerCostInput, error) { return nil, nil }
	t.Cleanup(func() { epicGraph, statusCI, statusWorkerCost = realGraph, realCI, realCost })

	vocabulary := map[string]bool{
		overviewStateHeld: true, overviewStateFailed: true, overviewStateRunning: true,
		overviewStateDone: true, overviewStateCancelled: true,
	}
	hegel.Test(t, func(ht *hegel.T) {
		repo := ht.TempDir()
		worldIDs := map[string]bool{}
		for n := hegel.Draw(ht, hegel.Integers(1, 5)); n > 0; n-- {
			runID := "epic-" + hegel.Draw(ht, hegel.Text().MinSize(3).MaxSize(3).Alphabet(
				"abcdefghijklmnopqrstuvwxyz"))
			worldIDs[runID] = true
			ending := hegel.Draw(ht, hegel.SampledFrom([]pbtTerminalKind{
				pbtEndDone, pbtEndFailed, pbtEndCancelled, pbtEndHeld,
			}))
			var steps []pbtStep
			switch ending {
			case pbtEndDone:
				steps = []pbtStep{{stage: reconcile.StageRunFinished, detail: "completed"}}
			case pbtEndFailed:
				steps = []pbtStep{{stage: reconcile.StageRunFinished,
					detail: "failed: " + pbtProse(ht)}}
			case pbtEndCancelled:
				steps = []pbtStep{{stage: reconcile.StageRunFinished,
					detail: "cancelled: " + pbtProse(ht)}}
			case pbtEndHeld:
				steps = []pbtStep{
					{stage: reconcile.StageRunHeld, tick: 1, attempt: 1,
						detail: hegel.Draw(ht, hegel.SampledFrom(pbtHoldDetails)) + ": a hold stands"},
					{stage: reconcile.StageRunFinished, detail: "failed: the run holds"},
				}
			}
			// The overview enumerates the checkout's own run directories beside
			// the machine's registry (localRunIDs), so a world run needs its
			// directory there as well as its feed.
			if err := os.MkdirAll(filepath.Join(repo, runstate.Root, "runs", runID), 0o755); err != nil {
				ht.Fatalf("create the run directory: %v", err)
			}
			feed := runfeed.Open(repo, runID)
			for i, step := range steps {
				tickID := ""
				var attempt *int
				if step.tick > 0 {
					tickID = fmt.Sprintf("t%d", step.tick)
				}
				if step.attempt > 0 {
					attempt = &step.attempt
				}
				event := runfeed.NewEvent(pbtBase.Add(time.Duration(i+1)*time.Minute),
					runID, tickID, attempt, step.stage, step.detail)
				if err := feed.Append(event); err != nil {
					ht.Fatalf("append the feed: %v", err)
				}
			}
		}
		ans := pbtInProcess([]string{"--json", "--repo", repo})
		if ans.code != exitSuccess {
			ht.Fatalf("the overview exited %d: %s", ans.code, ans.stderr)
		}
		doc := pbtHegelDoc(ht, ans)
		if got := doc["schema_version"]; got != float64(overviewSchemaVersion) {
			ht.Fatalf("the overview document's schema version is %v, want %d", got, overviewSchemaVersion)
		}
		rows, ok := doc["runs"].([]any)
		if !ok {
			ht.Fatalf("the overview document carries no runs:\n%v", doc)
		}
		if len(rows) != len(worldIDs) {
			ht.Fatalf("the overview lists %d runs for a world of %d", len(rows), len(worldIDs))
		}
		listed := map[string]bool{}
		for _, row := range rows {
			entry := row.(map[string]any)
			id, _ := entry["run_id"].(string)
			listed[id] = true
			state, _ := entry["state"].(string)
			if !vocabulary[state] {
				ht.Fatalf("the row for %s answers the state %q, a word the overview's vocabulary does not carry", id, state)
			}
		}
		for id := range worldIDs {
			if !listed[id] {
				ht.Fatalf("the overview lost run %s — a run the world holds vanished from the listing", id)
			}
		}
		// History rows sort after every row that is not: the glance's own
		// order, the one a person reads.
		seenHistory := false
		for _, row := range rows {
			entry := row.(map[string]any)
			if history, _ := entry["history"].(bool); history {
				seenHistory = true
				continue
			}
			if seenHistory {
				id, _ := entry["run_id"].(string)
				ht.Fatalf("the overview put the row %s, which needs somebody, after a history row", id)
			}
		}
	}, hegel.WithTestCases(40))
}

// ---------------------------------------------------------------------------
// P2 — the exit code is the table's, and agrees with the state word
// ---------------------------------------------------------------------------

// short: the exit table's agreement with the --json state word, over generated run states
func TestPBTExitCodeAgreesWithTheDocumentState(t *testing.T) {
	t.Parallel()
	table := pbtExitTableCodes()
	hegel.Test(t, func(ht *hegel.T) {
		w := genWorld(ht, true, false)
		repo := pbtWrite(ht, w)
		ans := pbtInProcess([]string{"watch", "--json", "--repo", repo, w.runID})
		if !table[ans.code] {
			ht.Fatalf("the watch exited %d, a code the documented table does not name\n%s\n%s",
				ans.code, pbtWorldString(w), ans.stderr)
		}
		doc := pbtHegelDoc(ht, ans)
		state, _ := doc["state"].(string)
		if state == "" {
			ht.Fatalf("the watch document carries no state word\n%s", pbtWorldString(w))
		}
		if want := stateExitClass(state); ans.code != want {
			ht.Fatalf("the watch exited %d for the state word %q, whose class is %d — the code and the word must agree\n%s\n%s",
				ans.code, state, want, pbtWorldString(w), ans.stderr)
		}
		if state != w.state {
			ht.Fatalf("the run ended %q by its own terminal word, but the document says %q\n%s\n%s",
				w.state, state, pbtWorldString(w), ans.stderr)
		}
		// The state word agrees with the document's own held half too: a
		// held answer carries the wait kind, and no other does.
		_, carriesHeld := doc["held"]
		if (state == agentStateHeld) != carriesHeld {
			ht.Fatalf("the document says %q while its held half is %v — the wait kind only a person moves belongs to the held answer alone\n%s",
				state, carriesHeld, pbtWorldString(w))
		}
	}, hegel.WithTestCases(60))
}

// short: status --json keeps its documented exception — the code is liveness's alone
func TestPBTStatusExitsLivenessAnswer(t *testing.T) {
	t.Parallel()
	table := pbtExitTableCodes()
	hegel.Test(t, func(ht *hegel.T) {
		w := genWorld(ht, false, true)
		repo := pbtWrite(ht, w)
		ans := pbtInProcess([]string{"status", "--json", "--repo", repo, w.runID})
		if !table[ans.code] {
			ht.Fatalf("the status exited %d, a code the documented table does not name\n%s", ans.code, pbtWorldString(w))
		}
		if w.missing {
			// A lookup that honestly came back empty: the family's own
			// answer is the failed class, with the stdout empty.
			if ans.code != exitGeneric {
				ht.Fatalf("a missing run answered %d, want the family's %d\n%s", ans.code, exitGeneric, pbtWorldString(w))
			}
			return
		}
		doc := pbtHegelDoc(ht, ans)
		liveness, ok := doc["liveness"].(map[string]any)
		if !ok {
			ht.Fatalf("the status document carries no liveness answer:\n%v\n%s", doc, pbtWorldString(w))
		}
		alive, _ := liveness["alive"].(bool)
		want := exitSuccess
		if !alive {
			want = exitGeneric
		}
		// The documented exception, held to exactly what it says: the exit
		// code is liveness's alone — 0 the run is alive, 1 it is not — and
		// never anything else the table names.
		if ans.code != want {
			ht.Fatalf("a run whose liveness says alive=%v exited %d, want liveness's own %d\n%s\n%s",
				alive, ans.code, want, pbtWorldString(w), ans.stderr)
		}
	}, hegel.WithTestCases(60))
}

// ---------------------------------------------------------------------------
// P3 — stdout is the document's alone, stderr is prose's alone
// ---------------------------------------------------------------------------

// short: the document never leaks to stderr, over generated run states and refusals
func TestPBTJSONKeepsTheDocumentOnStdout(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		command := hegel.Draw(ht, hegel.SampledFrom(pbtRunCommands))
		w := genWorld(ht, command == "watch", true)
		repo := pbtWrite(ht, w)
		ans := pbtInProcess([]string{command, "--json", "--repo", repo, w.runID})
		if strings.TrimSpace(ans.stdout) != "" {
			// Whatever the command wrote to stdout, it must parse as one
			// document — a success document or a failure document, never a
			// fragment.
			pbtHegelDoc(ht, ans)
		}
		if !pbtStderrIsProse(ans.stderr) {
			ht.Fatalf("%s --json wrote a document to stderr — stdout is the document's alone:\n%s\n%s",
				command, ans.stderr, pbtWorldString(w))
		}
		// The refusal shape: prose on stderr, empty stdout — the pair an
		// agent's parse depends on, in the direction that loses nothing.
		// `status` is again the exception: a feed it cannot parse is a
		// degraded document, not a refusal.
		if w.missing || (w.garbage && command != "status") {
			if strings.TrimSpace(ans.stderr) == "" {
				ht.Fatalf("%s --json refused (exit %d) with nothing on stderr — a refusal owes its prose\n%s",
					command, ans.code, pbtWorldString(w))
			}
		}
		if command == "watch" {
			return // watch has no --follow flag to refuse
		}
		// The usage refusal is the same pair: --json with --follow is the
		// documented refusal, in either order the argv spells it.
		first := hegel.Draw(ht, hegel.Booleans())
		var argv []string
		if first {
			argv = []string{command, "--follow", "--json", "--repo", repo, w.runID}
		} else {
			argv = []string{command, "--json", "--repo", repo, w.runID, "--follow"}
		}
		ans = pbtInProcess(argv)
		if strings.TrimSpace(ans.stdout) != "" || ans.code != exitUsage {
			ht.Fatalf("%s --json --follow answered %d with stdout %q — one document and a live stream are two shapes, and the refusal is the table's usage class\n%s",
				command, ans.code, ans.stdout, pbtWorldString(w))
		}
		if !pbtStderrIsProse(ans.stderr) {
			ht.Fatalf("the --json --follow refusal put a document on stderr:\n%s", ans.stderr)
		}
	}, hegel.WithTestCases(60))
}

// ---------------------------------------------------------------------------
// P4 — flags parse in any order (#75's remedy rule)
// ---------------------------------------------------------------------------

// pbtFlagGroups is each subject command's own flag surface: the flag groups
// a drawn invocation may carry — a flag and its value kept whole — and the
// one positional the command takes. The remedy rule is about flags AFTER the
// positional, so the positional is a group like any other and every
// permutation keeps each group intact.
var pbtFlagGroups = map[string][][]string{
	"events": {{"--repo", "%REPO%"}, {"--json"}, {"--follow"}, {"--from-start"}, {"--tail", "%N%"}, {"--from", "%N%"}},
	"status": {{"--repo", "%REPO%"}, {"--json"}, {"--follow"}, {"--interval", "%DUR%"}},
	"watch":  {{"--repo", "%REPO%"}, {"--json"}, {"--interval", "%DUR%"}},
}

// short: one invocation's flag groups permute without changing the answer
func TestPBTFlagsParseInAnyOrder(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		command := hegel.Draw(ht, hegel.SampledFrom(pbtRunCommands))
		w := genWorld(ht, true, true)
		repo := pbtWrite(ht, w)
		groups := pbtFlagGroups[command]
		drawn := [][]string{}
		for _, group := range groups {
			if hegel.Draw(ht, hegel.WeightedBooleans(0.4)) {
				drawn = append(drawn, group)
			}
		}
		// --json rides every invocation the property asks about.
		haveJSON := false
		for _, group := range drawn {
			if group[0] == "--json" {
				haveJSON = true
			}
		}
		if !haveJSON {
			drawn = append(drawn, []string{"--json"})
		}
		// --follow with --json is a refusal the property also holds: it
		// must be the SAME refusal in every order. Watch has no --follow.
		if command != "watch" && hegel.Draw(ht, hegel.WeightedBooleans(0.2)) {
			drawn = append(drawn, []string{"--follow"})
		}
		material := func(group []string) []string {
			out := make([]string, 0, len(group))
			for _, token := range group {
				switch token {
				case "%REPO%":
					out = append(out, repo)
				case "%N%":
					out = append(out, "2")
				case "%DUR%":
					out = append(out, "10ms")
				default:
					out = append(out, token)
				}
			}
			return out
		}
		flat := [][]string{}
		for _, group := range drawn {
			flat = append(flat, material(group))
		}
		flat = append(flat, []string{w.runID})
		// The base answer, flags-then-positional — the order Go's flag
		// package always parsed.
		base := pbtInProcess(append([]string{command}, pbtFlatten(flat)...))
		// Every permutation of the same invocation answers the same: the
		// positional may sit anywhere, and a flag group may follow it — the
		// remedy rule, and the shape the #75 bug refused. A group list six
		// long has 720 orders; the property draws a bounded sample — every
		// order is one draw, shrinkable, and the two orders that matter most
		// are drawn too: the positional FIRST (the #75 shape) and the
		// reversal.
		stableBase := pbtStableStdout(base.stdout)
		sameAnswer := func(order [][]string, label string) {
			ans := pbtInProcess(append([]string{command}, pbtFlatten(order)...))
			if ans.code != base.code || pbtStableStdout(ans.stdout) != stableBase {
				ht.Fatalf("%s answered differently when its flags moved (exit %d vs %d, %s):\n  argv: %v\n%s\n--base stdout--\n%s\n--this stdout--\n%s",
					command, base.code, ans.code, label, pbtFlatten(order), pbtWorldString(w), base.stdout, ans.stdout)
			}
			if !pbtStderrIsProse(ans.stderr) {
				ht.Fatalf("%s leaked a document to stderr in one order but not another:\n%s", command, ans.stderr)
			}
		}
		for k := 0; k < 8; k++ {
			order := append([][]string{}, flat...)
			for i := len(order) - 1; i > 0; i-- {
				j := hegel.Draw(ht, hegel.Integers(0, i))
				order[i], order[j] = order[j], order[i]
			}
			sameAnswer(order, "drawn order")
		}
		// The two named orders: the #75 shape (every flag group after the
		// positional) and the full reversal.
		positionalFirst := append([][]string{}, flat[len(flat)-1])
		positionalFirst = append(positionalFirst, flat[:len(flat)-1]...)
		sameAnswer(positionalFirst, "positional first")
		reversed := make([][]string, 0, len(flat))
		for i := len(flat) - 1; i >= 0; i-- {
			reversed = append(reversed, flat[i])
		}
		sameAnswer(reversed, "reversed")
	}, hegel.WithTestCases(30))
}

// pbtFlatten joins the groups into one argv tail.
func pbtFlatten(groups [][]string) []string {
	var out []string
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// pbtVolatileKeys are the document fields that carry the wall clock the
// answer is stamped by: generated_at and the ages the answer measures
// against it. Two invocations of one argv differ in them alone, so the
// flag-order property compares answers with them stripped — a parse that
// drops a flag changes the answer's SHAPE, and the shape is what the remedy
// rule is about.
var pbtVolatileKeys = map[string]bool{"generated_at": true, "last_event_age_seconds": true}

// pbtStableStdout strips the volatile stamps from one answer's document and
// re-marshals it canonically. Prose and empty stdouts pass through whole.
func pbtStableStdout(stdout string) string {
	var doc any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return stdout
	}
	pbtStripVolatile(&doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		return stdout
	}
	return string(raw)
}

func pbtStripVolatile(value *any) {
	switch typed := (*value).(type) {
	case map[string]any:
		for key, inner := range typed {
			if pbtVolatileKeys[key] {
				delete(typed, key)
				continue
			}
			pbtStripVolatile(&inner)
			typed[key] = inner
		}
	case []any:
		for i := range typed {
			pbtStripVolatile(&typed[i])
		}
	}
}

// ---------------------------------------------------------------------------
// P5 — a hold the run's answer does not carry never alerts
// ---------------------------------------------------------------------------

// short: the hold alert fires exactly when the run's own answer is held
func TestPBTWatchAlertsAHoldExactlyWhenTheRunHoldsIt(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		w := genWorld(ht, true, true)
		repo := pbtWrite(ht, w)
		// The plain stream is the surface the alert is prose on; --json is
		// the surface an agent reads. Both owe the same agreement: the
		// alert appears exactly when the answer is held.
		for _, argv := range [][]string{
			{"watch", "--repo", repo, w.runID},
			{"watch", "--json", "--repo", repo, w.runID},
		} {
			ans := pbtInProcess(argv)
			alerted := strings.Contains(ans.stderr, "is HOLDING")
			if w.state == agentStateHeld {
				if !alerted {
					ht.Fatalf("a run that ended holding %q never alerted it — the watch's whole reason to exist\n%s\nstderr:\n%s",
						w.state, pbtWorldString(w), ans.stderr)
				}
				// The alert carries the ONE command that clears the hold —
				// addressed to this run, never a placeholder.
				if !strings.Contains(ans.stderr, "ticfac settle") && !strings.Contains(ans.stderr, "ticfac triage") {
					ht.Fatalf("the hold alert names no command that clears it:\n%s\nstderr:\n%s",
						pbtWorldString(w), ans.stderr)
				}
				if strings.Contains(ans.stderr, "<epic-id>") || strings.Contains(ans.stderr, "<tick-id>") {
					ht.Fatalf("the hold alert prints a placeholder instead of this run's own ids:\n%s", ans.stderr)
				}
			} else if alerted {
				ht.Fatalf("the run's own answer is %q (exit %d) and its hold was answered by a resume=%v, but the watch alerted a hold anyway — a stale hold is the previous incarnation's history, never the current one's alarm\n%s\nstderr:\n%s",
					w.state, ans.code, w.answeredHold, pbtWorldString(w), ans.stderr)
			}
		}
	}, hegel.WithTestCases(60))
}

// ---------------------------------------------------------------------------
// P6 — a growing feed never lies (the stateful property)
// ---------------------------------------------------------------------------

// short: every prefix of a generated timeline answers exactly what it holds
func TestPBTAStandingFeedAnswersExactlyWhatItTook(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		w := genWorld(ht, false, true)
		if w.missing || w.garbage || w.state != "" {
			// The state machine this property walks is the feed's growth;
			// a terminal line ends the walk, so the world here is always
			// an open one, drawn directly.
			w = pbtWorld{runID: "epic-" + hegel.Draw(ht, hegel.Text().MinSize(3).MaxSize(3).Alphabet(
				"abcdefghijklmnopqrstuvwxyz"))}
			for i := 0; i < hegel.Draw(ht, hegel.Integers(2, 6)); i++ {
				w.steps = append(w.steps, pbtStep{
					stage:   hegel.Draw(ht, hegel.SampledFrom([]string{reconcile.StageDispatched, reconcile.StageGateFailed})),
					detail:  pbtProse(ht),
					tick:    hegel.Draw(ht, hegel.Integers(1, 4)),
					attempt: hegel.Draw(ht, hegel.Integers(1, 3)),
				})
			}
		}
		repo := ht.TempDir()
		feed := runfeed.Open(repo, w.runID)
		for i, step := range w.steps {
			if err := feed.Append(runfeed.NewEvent(
				pbtBase.Add(time.Duration(i+1)*time.Minute), w.runID,
				fmt.Sprintf("t%d", step.tick), &step.attempt, step.stage, step.detail)); err != nil {
				ht.Fatalf("append the feed: %v", err)
			}
			// After every prefix: the document answers with exactly the
			// events that stand — count, order, stages — and nothing else.
			ans := pbtInProcess([]string{"events", "--json", "--repo", repo, w.runID})
			if ans.code != exitSuccess {
				ht.Fatalf("events --json exited %d on a feed of %d lines:\n%s", ans.code, i+1, ans.stderr)
			}
			doc := pbtHegelDoc(ht, ans)
			events, ok := doc["events"].([]any)
			if !ok || len(events) != i+1 {
				ht.Fatalf("after %d lines the document answers %d events — a growing feed never loses or invents a line\n%s",
					i+1, len(events), pbtWorldString(w))
			}
			for j, raw := range events {
				event := raw.(map[string]any)
				want := w.steps[j]
				if event["stage"] != want.stage || event["run_id"] != w.runID {
					ht.Fatalf("the document's event %d is %v, want stage %s of run %s — order is the feed's own\n%s",
						j, event, want.stage, w.runID, pbtWorldString(w))
				}
			}
		}
	}, hegel.WithTestCases(40))
}

// ---------------------------------------------------------------------------
// Non-vacuity: each property fails against its seeded bug
// ---------------------------------------------------------------------------

// pbtCheck is one property as a plain check over one world, one argv and one
// answer — the shape the seeded-bug harness needs (hegel draws; the harness
// fixes the worlds).
type pbtCheck func(w pbtWorld, argv []string, ans pbtAnswer) error

// TestAgentJSONPropertiesCatchSeededBugs: non-vacuity, proven in the gate.
// Each property runs against its deliberately broken CLI and must fail for
// at least one generated world — a property that cannot fail pins nothing,
// so the test refuses one.
func TestAgentJSONPropertiesCatchSeededBugs(t *testing.T) {
	t.Parallel()

	oneDocument := func(w pbtWorld, argv []string, ans pbtAnswer) error {
		if w.missing || w.garbage || !containsFlag(argv, "--json") {
			return nil
		}
		doc, err := pbtDecodeOne(ans.stdout)
		if err != nil {
			return fmt.Errorf("no document: %w", err)
		}
		switch argv[0] {
		case "watch":
			if doc["schema"] != agentSchemaID("watch") {
				return fmt.Errorf("wrong schema")
			}
		case "events":
			if doc["schema"] != agentSchemaID("events") {
				return fmt.Errorf("wrong schema")
			}
		case "status":
			if doc["schema_version"] != float64(statusmodel.SchemaVersion) {
				return fmt.Errorf("wrong schema version")
			}
		}
		return nil
	}
	exitAgrees := func(w pbtWorld, argv []string, ans pbtAnswer) error {
		if w.missing || w.garbage || argv[0] != "watch" || !containsFlag(argv, "--json") {
			return nil
		}
		doc, err := pbtDecodeOne(ans.stdout)
		if err != nil {
			return nil // the one-document property owns the parse failure
		}
		state, _ := doc["state"].(string)
		if state == "" || ans.code != stateExitClass(state) || state != w.state {
			return fmt.Errorf("code %d disagrees with state %q (world %q)", ans.code, state, w.state)
		}
		return nil
	}
	stdoutAlone := func(_ pbtWorld, argv []string, ans pbtAnswer) error {
		if !pbtStderrIsProse(ans.stderr) {
			return fmt.Errorf("a document leaked to stderr")
		}
		if strings.TrimSpace(ans.stdout) != "" {
			if _, err := pbtDecodeOne(ans.stdout); err != nil {
				return fmt.Errorf("stdout is not one document: %w", err)
			}
		}
		return nil
	}
	anyOrder := func(w pbtWorld, argv []string, ans pbtAnswer) error {
		base := pbtInProcess(argv)
		if ans.code != base.code || pbtStableStdout(ans.stdout) != pbtStableStdout(base.stdout) {
			return fmt.Errorf("the answer changed with the flag order")
		}
		return nil
	}
	alertAgrees := func(w pbtWorld, argv []string, ans pbtAnswer) error {
		if w.missing || w.garbage || argv[0] != "watch" {
			return nil
		}
		alerted := strings.Contains(ans.stderr, "is HOLDING")
		if (w.state == agentStateHeld) != alerted {
			return fmt.Errorf("alert=%v for a run whose answer is %q", alerted, w.state)
		}
		return nil
	}
	exactlyWhatItTook := func(w pbtWorld, argv []string, ans pbtAnswer) error {
		if w.missing || w.garbage || argv[0] != "events" || !containsFlag(argv, "--json") {
			return nil
		}
		doc, err := pbtDecodeOne(ans.stdout)
		if err != nil {
			return nil
		}
		events, _ := doc["events"].([]any)
		if len(events) != len(w.steps) {
			return fmt.Errorf("the document answers %d events for a feed of %d", len(events), len(w.steps))
		}
		return nil
	}

	// The breakers: each one violates exactly one property, the way the real
	// defects did.
	breakers := []struct {
		name  string
		wrap  func(next pbtRunner) pbtRunner
		holds pbtCheck
	}{
		{"one document names its schema", func(next pbtRunner) pbtRunner {
			return func(argv []string) pbtAnswer {
				ans := next(argv)
				if containsFlag(argv, "--json") {
					ans.stdout = strings.Replace(ans.stdout, `"schema"`, `"scheme"`, 1)
					ans.stdout = strings.Replace(ans.stdout, `"schema_version"`, `"scheme_version"`, 1)
				}
				return ans
			}
		}, oneDocument},
		{"the exit code stays on the table and agrees", func(next pbtRunner) pbtRunner {
			return func(argv []string) pbtAnswer {
				ans := next(argv)
				ans.code = 42
				return ans
			}
		}, exitAgrees},
		{"the document never leaks to stderr", func(next pbtRunner) pbtRunner {
			return func(argv []string) pbtAnswer {
				ans := next(argv)
				if strings.TrimSpace(ans.stdout) != "" && containsFlag(argv, "--json") {
					ans.stderr = ans.stderr + ans.stdout
				}
				return ans
			}
		}, stdoutAlone},
		{"flags parse in any order", func(next pbtRunner) pbtRunner {
			// #75's exact bug: the parse stops at the first positional, so
			// every flag group after it is read as another argument and the
			// invocation is refused.
			return func(argv []string) pbtAnswer {
				for i, token := range argv[1:] {
					if !strings.HasPrefix(token, "-") {
						if i+2 < len(argv) {
							return next(argv[:i+2])
						}
						break
					}
				}
				return next(argv)
			}
		}, anyOrder},
		{"the alert agrees with the answer", func(next pbtRunner) pbtRunner {
			return func(argv []string) pbtAnswer {
				ans := next(argv)
				if argv[0] == "watch" {
					ans.stderr = ans.stderr +
						"\nticfac watch: run r-x is HOLDING t1 try 1 (run dispatch #1) for a person:\nsomeone must decide\n\n"
				}
				return ans
			}
		}, alertAgrees},
		{"a growing feed never loses a line", func(next pbtRunner) pbtRunner {
			return func(argv []string) pbtAnswer {
				ans := next(argv)
				if argv[0] != "events" || !containsFlag(argv, "--json") {
					return ans
				}
				doc, err := pbtDecodeOne(ans.stdout)
				if err != nil {
					return ans
				}
				events, _ := doc["events"].([]any)
				if len(events) > 1 {
					doc["events"] = events[:len(events)-1]
					if raw, err := json.MarshalIndent(doc, "", "  "); err == nil {
						ans.stdout = string(raw) + "\n"
					}
				}
				return ans
			}
		}, exactlyWhatItTook},
	}

	// A deterministic population: the same generator's shapes, one seed per
	// world, so the population is the same on every host and every run.
	worlds := pbtSeedWorlds(40)
	caught := map[string]bool{}
	for _, breaker := range breakers {
		runner := breaker.wrap(pbtInProcess)
		for _, w := range worlds {
			repo := pbtWrite(t, w)
			for _, argv := range pbtWorldAsks(w, repo) {
				if err := breaker.holds(w, argv, runner(argv)); err != nil {
					caught[breaker.name] = true
					break
				}
			}
			if caught[breaker.name] {
				break
			}
		}
	}
	for _, breaker := range breakers {
		if !caught[breaker.name] {
			t.Errorf("the property for %q never failed against its seeded broken CLI — the check is vacuous", breaker.name)
		}
	}
}

// containsFlag says whether the argv carries the flag, in any spelling.
func containsFlag(argv []string, flag string) bool {
	for _, token := range argv {
		if token == flag || strings.HasPrefix(token, flag+"=") {
			return true
		}
	}
	return false
}

// pbtWorldAsks is every argv the harness asks about one world: the three
// surfaces, --json and plain, and the spellings with a flag group AFTER the
// positional — the shape the remedy rule is about. An open feed only answers
// the one-shot asks (the watch would follow it); a missing or garbage world
// answers everything with a refusal.
func pbtWorldAsks(w pbtWorld, repo string) [][]string {
	asks := [][]string{
		{"events", "--json", "--repo", repo, w.runID},
		{"status", "--json", "--repo", repo, w.runID},
		{"events", "--repo", repo, "--json", w.runID},
		{"status", "--repo", repo, "--json", w.runID},
		{"events", w.runID, "--json", "--repo", repo},
		{"status", w.runID, "--json", "--repo", repo},
	}
	if w.state != "" || w.missing || w.garbage {
		asks = append(asks,
			[]string{"watch", "--json", "--repo", repo, w.runID},
			[]string{"watch", "--repo", repo, "--json", w.runID},
			[]string{"watch", w.runID, "--json", "--repo", repo},
			[]string{"watch", "--repo", repo, w.runID},
		)
	}
	return asks
}

// pbtSeedWorlds draws the seeded-bug harness's fixed population: the same
// generator's shapes, one seed per world, deterministic on every host.
func pbtSeedWorlds(n int) []pbtWorld {
	out := make([]pbtWorld, 0, n)
	for s := uint64(1); s <= uint64(n); s++ {
		r := rand.New(rand.NewPCG(s, s))
		w := pbtWorld{}
		if r.IntN(2) == 0 {
			w.runID = "epic-" + pbtRandLetters(r)
		} else {
			w.runID = "run-" + pbtRandLetters(r)
		}
		if r.IntN(6) == 0 {
			w.missing = true
			out = append(out, w)
			continue
		}
		for i := 0; i < 1+r.IntN(3); i++ {
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageDispatched,
				detail: pbtRandLetters(r), tick: 1 + r.IntN(4), attempt: 1 + r.IntN(3)})
		}
		switch r.IntN(7) {
		case 0: // open
		case 1:
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished, detail: "completed"})
			w.state = agentStateDone
		case 2:
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished,
				detail: "failed: " + pbtRandLetters(r)})
			w.state = agentStateFailed
		case 3:
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished,
				detail: "cancelled: " + pbtRandLetters(r)})
			w.state = agentStateCancelled
		case 4: // a hold that stands
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunHeld, tick: 1, attempt: 1,
				detail: pbtHoldDetails[r.IntN(len(pbtHoldDetails))] + ": " + pbtRandLetters(r)})
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished,
				detail: "failed: " + pbtRandLetters(r)})
			w.state = agentStateHeld
		case 5: // a hold a resume answered, then a clean end
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunHeld, tick: 1, attempt: 1,
				detail: pbtHoldDetails[r.IntN(len(pbtHoldDetails))] + ": " + pbtRandLetters(r)})
			w.steps = append(w.steps, pbtStep{stage: runfeed.StageResumed, detail: pbtRandLetters(r)})
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished, detail: "completed"})
			w.state = agentStateDone
			w.answeredHold = true
		case 6: // a hold a resume answered, then a failed end
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunHeld, tick: 1, attempt: 1,
				detail: pbtHoldDetails[r.IntN(len(pbtHoldDetails))] + ": " + pbtRandLetters(r)})
			w.steps = append(w.steps, pbtStep{stage: runfeed.StageResumed, detail: pbtRandLetters(r)})
			w.steps = append(w.steps, pbtStep{stage: reconcile.StageRunFinished,
				detail: "failed: " + pbtRandLetters(r)})
			w.state = agentStateFailed
			w.answeredHold = true
		}
		out = append(out, w)
	}
	return out
}

func pbtRandLetters(r *rand.Rand) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz "
	id := make([]byte, 4+r.IntN(20))
	for i := range id {
		id[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(id)
}
