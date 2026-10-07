package reconcile

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
	"github.com/pengelbrecht/ticfac/internal/tk"
	"hegel.dev/go/hegel"
)

// A first stateful model of the reconciler (tick 89g), driven by Hegel's
// state-machine testing over the two fakes this suite already owns: the fake
// tracker (a real file the reconciler's tracker seam answers from, with tk's
// own claim and width arithmetic) and the fake runner's answer vocabulary
// (succeeded, failed, no report).
//
// WHY A MODEL, AND WHAT IS REAL IN IT. The reconciler itself is inseparable
// from git: every step it takes is a compare-and-swap on a ref. Driving the
// whole of it as a property would mean building a repository per generated
// case — the e2e suite's cost for one hand-picked sequence. What PBT buys
// here is the SEQUENCE space: arbitrary interleavings of the eight operations
// the tick names. That only needs the decisions, not the plumbing, so the
// model holds the durable facts a reconciler reads (the tracker's graph and
// claims, the attempts it left on origin, its checkpoint states, the
// integration branch's head) and, every place the real package has a decision
// over those facts, the model calls the real one:
//
//   - ADMISSION is r.mayAdmit — the width, the wave boundary, the role jobs,
//     the substrate capacity, the adoption path — over the real held window
//     and real planEntry values, against the fake tracker's own claim count,
//     which refuses claims beyond the declared width exactly as tk does.
//   - THE PLANNING ORDER is planFrom and sortPlan over the fake tracker's
//     real graph layering; a tick created mid-run enters through
//     unplannedTicks, the same function replan admits it with.
//   - THE LIFECYCLE GUARDS are the real ones: MayDispatch before every
//     dispatch (A11), Poll as the keepalive (A4), ClampBudget at admission
//     (A12), RecordEvidence and PublishEvidence around the gate's verdict
//     (A13).
//   - A TRANSIENT REMOTE FAILURE is classified and waited through by
//     runstate's own RemoteRetry over the real ClassifyRemote.
//   - A SAME-WAVE MERGE CONFLICT, a failed collect and a missing report are
//     disposed of through the real hold vocabulary: holdsOnlyItsTick,
//     redispatchesInRun, refusedTickState, holdsForAPerson.
//   - THE ABSORPTION BOUND is absorptionDepthExceeded over a chain built the
//     way absorptionChain walks one.
//
// What is the model's own is the integration branch itself — a content string
// moved by compare-and-swap, with the one fault a rule arms (a transient
// failure, a lost acknowledgement, a genuinely foreign write) — and the
// adoption of a live attempt after a crash, which in the real code reads
// markers out of a git store and here reads the model's durable attempt
// records. Both are stated in o82's and adoptionFirst's own shapes, and the
// invariants are about the OUTCOMES, so a model transition that drifted from
// the real one is caught by the invariant it breaks.
//
// The five invariants are the tick's own words, checked at every join point:
//
//   - A LIVE JOB IS NEVER REDISPATCHED: no start ever happens for a tick that
//     already has a live attempt of this run — an attempt that exists is
//     adopted, never started over (the #50 resume order, and the
//     never_redispatch_live guard's own name).
//   - NOTHING CLOSES BEHIND A FAILING GATE: every close in the model's
//     history stands on a gate that passed over the tree as it stood when the
//     close happened, published through the real A13 seam.
//   - THE WIDTH IS NEVER EXCEEDED: the fake tracker's own claim count — open
//     claims and the high-water mark — never passes the declared width,
//     counted the way tk counts: a claim lives until its tick closes.
//   - A COLD RESTART REACHES THE SAME STATE: an incarnation that lost all its
//     memory re-derives, from the durable records alone, the same live
//     attempts, the same claims and the same work left to do.
//   - ABSORPTION STAYS BOUNDED: past the recorded bound a finding is deferred
//     to the backlog, never recursed into — and a deferral never becomes work
//     the run dispatches.

// short: an in-memory state machine over the real window/claim/gate decisions and the fake tracker, no git, no processes

// modelAttempt is one attempt's durable marker as origin holds it: the thing
// a restarted incarnation adopts by identity.
type modelAttempt struct {
	tick     string
	attempt  int
	state    string // dispatched | settled | closed | rejected
	terminal bool   // the worker is gone
	// claimGen is the generation of the last graph read this run's claim was
	// taken in: a claim newer than the freshest reading is one the width's
	// arithmetic can only see through the window holding it.
	claimGen int
}

// modelClose is one close, with the tree and gate facts it stood on: how many
// merges the integration branch carried when it happened, and how many the
// published gate verdict had covered.
type modelClose struct {
	tick                string
	merges, gateCovered int
}

// modelStart is one start, with the live attempts of this run at the moment it
// happened — the fact "a live job was never redispatched" is made of.
type modelStart struct {
	tick string
	live []string
}

// modelGate is a gate's durable verdict: what it evaluated, whether it
// passed, and whether its publication survived.
type modelGate struct {
	tick      string
	covered   int
	key       string
	finger    Fingerprint
	passed    bool
	published bool
}

// modelOrigin is the remote the model writes to: named refs whose content
// moves by compare-and-swap, and the one fault a rule arms for the next write.
type modelOrigin struct {
	refs   map[string]string
	fault  string // "" | "transient" | "lost_ack" | "foreign"
	writes int
}

func (o *modelOrigin) get(ref string) string {
	if content, ok := o.refs[ref]; ok {
		return content
	}
	return "base"
}

// lifecycleModel is the reconciler's world and the run's own state over it.
type lifecycleModel struct {
	tr    *fakeTracker
	r     *Reconciler
	width int
	ctx   context.Context

	// DURABLE — what a crash leaves behind and the next incarnation reads.
	attempts map[string]*modelAttempt // tick -> this run's attempt on origin
	origin   *modelOrigin             // the integration branch and the markers
	gates    map[string]*modelGate    // tick -> the standing gate verdict
	absorbed []chainLink              // the absorption chain this run built
	deferred int                      // findings deferred past the bound
	starts   []modelStart
	closes   []modelClose
	feed     []Event  // the run's durable event stream, across every incarnation
	log      []string // the operation log, for the counterexample

	// IN-MEMORY — lost at a crash, re-derived from the durable half.
	window        held
	plan          []planEntry
	queue         []planEntry
	parked        map[string]bool
	stopped       bool
	conflictNext  bool // the next merge cannot be resolved
	gateFailsNext bool // the next gate run refuses the tree

	// What a cold restart must re-derive, captured at the crash.
	restartLive  []string
	restartClaim []string
	restarted    bool

	// The width's readings and the claims parked out of them. readGen is the
	// generation of the last fresh read of the graph the width's arithmetic
	// consumes (run start, restart, replan); parkedGen records, per parked
	// tick, the generation its claim was taken in — the two together say
	// which parked claims the arithmetic cannot see.
	readGen   int
	parkedGen map[string]int
	// overClaims records every claim the tracker refused as past the width,
	// with whether an invisible parked claim explains it: the known
	// counterexample the width invariant records rather than hides (see
	// InvariantTheDeclaredWidthIsNeverExceeded).
	overClaims []bool

	// Coverage, so the report can say which half of each property ran.
	counts map[string]int

	// armAlways bypasses the fault rules' coins, and forceOutcome the settle
	// rule's draw, for the one fixed walk that exercises every operation
	// deterministically: the walk states what it wants to happen, the property
	// run leaves the faults and the workers' answers to the draw.
	armAlways    bool
	forceOutcome string
	// forceCut arms one finish's cut between its gate and its close, for the
	// walk: the next finish is interrupted after its verdict, and the one
	// after takes it up from the standing evidence.
	forceCut bool
}

// newLifecycleModel builds one case's world: a drawn epic graph layered by
// the fake tracker, a drawn width the tracker enforces, and the reconciler's
// decision surface on hand-built state.
func newLifecycleModel(t *testing.T, dir string, tc hegel.TestCase) *lifecycleModel {
	t.Helper()
	tracker := newTracker(t, dir)

	// The epic graph: a drawn set of work ticks, the review behind all of
	// them, the close-out behind the review — layered by the tracker's own
	// wave layering, which is what a restart re-derives its plan from.
	state, err := tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Ticks = map[string]tk.Tick{}
	state.Roles = map[string]string{"rv": "review", "co": "closeout"}
	state.Order = nil
	state.Waves = nil
	state.BlockedBy = map[string][]string{}
	var work []string
	for i, n := 0, hegel.Draw(tc, hegel.Integers(2, 4)); i < n; i++ {
		id := fmt.Sprintf("w%d", i+1)
		state.Order = append(state.Order, id)
		state.Ticks[id] = tk.Tick{ID: id, Title: "work " + id, Status: "open", Type: "task",
			Parent: state.Epic, Priority: 2}
		work = append(work, id)
	}
	state.Order = append(state.Order, "rv", "co")
	state.Ticks["rv"] = tk.Tick{ID: "rv", Title: "review", Status: "open", Type: "task", Parent: state.Epic, Priority: 2}
	state.Ticks["co"] = tk.Tick{ID: "co", Title: "close-out", Status: "open", Type: "task", Parent: state.Epic, Priority: 2}
	state.BlockedBy["rv"] = append([]string{}, work...)
	state.BlockedBy["co"] = append(append([]string{}, work...), "rv")
	tracker.write(t, state)

	// The declared width, enforced by the tracker the way tk enforces it.
	width := hegel.Draw(tc, hegel.Integers(1, 3))
	tracker.refuseClaimsBeyond(width)

	m := &lifecycleModel{
		tr: tracker, width: width, ctx: context.Background(),
		attempts: map[string]*modelAttempt{},
		origin:   &modelOrigin{refs: map[string]string{}},
		gates:    map[string]*modelGate{},
		parked:   map[string]bool{}, parkedGen: map[string]int{}, counts: map[string]int{},
	}
	// The reconciler's decision surface: the in-memory half the guards, the
	// window and the checkpoint read, in the state New leaves them in — with
	// no repository under it, because nothing this model calls touches git.
	m.r = &Reconciler{
		opts:          Options{EpicID: state.Epic, Owner: "ticfac-test"},
		runID:         "r-pbt",
		hostWidth:     width,
		guardsOff:     map[string]bool{},
		holds:         map[string]*hold{},
		lastPolled:    map[string]time.Time{},
		liveness:      map[string]string{},
		evidence:      map[string]Fingerprint{},
		published:     []string{},
		wipeThreshold: DefaultWipeThreshold,
		now:           func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
		sleep:         func(time.Duration) {},
		ticks:         []runstate.TickState{},
	}
	m.r.inFlightIDs = m.inFlightIDs()
	m.plan = m.planFromTracker()
	m.queue = append([]planEntry{}, m.plan...)
	return m
}

// record is the model's every event: the journal this incarnation keeps AND
// the feed, which is durable across incarnations — an invariant that asks
// "did the gate run before the close" must read the feed, because the journal
// of the incarnation that ran the gate is gone.
func (m *lifecycleModel) record(tick, stage, format string, args ...any) {
	event := Event{At: m.r.now(), Tick: tick, Stage: stage, Detail: fmt.Sprintf(format, args...)}
	m.feed = append(m.feed, event)
	m.r.record(tick, stage, format, args...)
}

// logf appends one line to the operation log, so a counterexample says what
// the machine did in the order it did it.
func (m *lifecycleModel) logf(format string, args ...any) {
	m.log = append(m.log, fmt.Sprintf(format, args...))
}

// fail panics with the invariant's evidence: Hegel treats a panic in a rule or
// invariant as an interesting failure and shrinks the case to it.
func (m *lifecycleModel) fail(format string, args ...any) {
	panic(fmt.Sprintf(format, args...) + "\nthe machine's log:\n  " + strings.Join(m.log, "\n  "))
}

// ---------------------------------------------------------------- reads ---

// trackerState is the tracker's state file.
func (m *lifecycleModel) trackerState() trackerState {
	state, err := m.tr.load()
	if err != nil {
		m.fail("the tracker's state cannot be read: %v", err)
	}
	return state
}

// epicID is the epic this run works.
func (m *lifecycleModel) epicID() string { return m.trackerState().Epic }

// graph is a fresh read of the epic graph, the one thing a plan is derived
// from and re-derived from.
func (m *lifecycleModel) graph() tk.Graph {
	graph, err := m.tr.Graph(m.ctx, m.epicID())
	if err != nil {
		m.fail("the tracker refused its graph: %v", err)
	}
	return graph
}

// inFlightIDs is the graph's claimed-and-not-open children — the width's raw
// material since tk 0.32.0 (tick dz1): the tracker's own count of every claim
// under the epic, whoever holds it.
func (m *lifecycleModel) inFlightIDs() []string { return m.graph().Dispatch.InFlightIDs }

// planFromTracker re-derives the plan from a fresh read of the graph: the real
// layering, the real order, and the real adoption facts — a tick whose
// checkpoint row reads dispatched, reported or integrated is one this run
// already has an attempt of (adoptionFirst's own rank), so it is adopted
// rather than claimed again.
func (m *lifecycleModel) planFromTracker() []planEntry {
	m.readGen++ // a fresh read of the graph is the width arithmetic's generation
	plan := planFrom(m.graph())
	for i := range plan {
		// adoptionFirst's facts, restated: a tick whose checkpoint row reads
		// dispatched, reported or integrated is one this run has an attempt
		// of, and a claimed tick this run dispatched before is its OWN claim
		// (dz1) — never a foreign party's.
		if _, dispatched := m.attempts[plan[i].TickID]; dispatched {
			plan[i].OwnClaim = plan[i].Claimed
			switch m.r.tickState(plan[i].TickID) {
			case "dispatched", "reported", "integrated":
				plan[i].InFlight, plan[i].Claimed = true, true
			}
		}
	}
	// adoptionFirst's order, restated: this run's live attempts first, the
	// rest as planned. The real one reads the attempt records out of a git
	// store; the model reads the same fact from its durable markers.
	sort.SliceStable(plan, func(i, j int) bool {
		return adoptionRank(plan[i]) < adoptionRank(plan[j])
	})
	return plan
}

// adoptionRank is adoptionFirst's rank for one entry: 0 for a tick this run
// has a live attempt of, 1 for a claimed tick, 2 for fresh work, 3 for the
// role jobs that run alone.
func adoptionRank(entry planEntry) int {
	switch {
	case isRoleJob(entry.Role):
		return 3
	case entry.InFlight:
		return 0
	case entry.StaleClaim || entry.Claimed:
		return 1
	}
	return 2
}

// liveOf says whether this run has a live attempt of one tick: the fact a
// dispatch must adopt rather than start over.
func (m *lifecycleModel) liveOf(tick string) bool {
	a, ok := m.attempts[tick]
	return ok && !a.terminal && a.state != "closed" && a.state != "rejected"
}

// liveTicks is every tick with a live attempt, sorted.
func (m *lifecycleModel) liveTicks() []string {
	var out []string
	for tick := range m.attempts {
		if m.liveOf(tick) {
			out = append(out, tick)
		}
	}
	sort.Strings(out)
	return out
}

// --------------------------------------------------------- the durable ---

// push moves one ref from expected to next under the compare-and-swap, waiting
// through a TRANSIENT failure with runstate's own bounded retry over the real
// ClassifyRemote. The fault a rule armed decides what the remote does this
// time.
func (m *lifecycleModel) push(what, ref, expected, next string) error {
	retry := runstate.RemoteRetry{Attempts: 4, Backoff: time.Millisecond,
		Sleep:  func(time.Duration) {},
		Jitter: func(time.Duration) time.Duration { return 0 },
		Report: func(n runstate.RemoteRetryNotice) { m.logf("waited through %s (class %v)", n.What, n.Class) },
		Failed: func(f runstate.RemoteFailure) { m.logf("%s failed as %s: %v", f.What, f.Class, f.Err) }}
	return retry.Do(what, func() error {
		switch m.origin.fault {
		case "transient":
			// The blip passes: the same push goes through on the retry, and
			// the run's wall clock is the only thing that was spent.
			m.origin.fault = ""
			return fmt.Errorf("git push: exit status 128: Connection reset by remote.example.com port 22\n" +
				"fatal: Could not read from remote repository.")
		case "lost_ack":
			// The write LANDED and the acknowledgement never came back (tick
			// o82): the ref carries the content, the writer believes it does not.
			m.origin.refs[ref] = next
			m.origin.writes++
			m.origin.fault = ""
			return fmt.Errorf("runstate: push %s: conflict_stale_sha: the run state moved under this reconciler", what)
		case "foreign":
			// A genuinely foreign change, the one a lost ack must not be read as.
			m.origin.refs[ref] = "a change another party wrote"
			m.origin.fault = ""
			return fmt.Errorf("runstate: push %s: conflict_stale_sha: the run state moved under this reconciler", what)
		}
		if m.origin.get(ref) != expected {
			return fmt.Errorf("runstate: push %s: conflict_stale_sha: the run state moved under this reconciler", what)
		}
		m.origin.refs[ref] = next
		m.origin.writes++
		return nil
	})
}

// write is the model's every durable write: the push above, and the re-read a
// conflict_stale_sha owes (tick o82). A head that carries exactly the content
// this writer was writing is this writer's own lost ack and the run goes on;
// only a genuinely foreign change is a conflict.
func (m *lifecycleModel) write(what, ref, expected, next string) error {
	err := m.push(what, ref, expected, next)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "conflict_stale_sha") {
		if m.origin.get(ref) == next {
			m.logf("%s: the ack was lost, not the write — origin carries exactly this writer's content, so the run continues", what)
			return nil
		}
		m.logf("%s: a genuinely foreign change is on the ref — a conflict, not a lost ack", what)
	}
	return err
}

// ------------------------------------------------------------ the rules ---

// RuleDispatchTheNextQueuedTick dispatches the queue's head the way the window
// does: the strike-out check, the admission decision, the claim, the marker on
// origin BEFORE the job is started, and the start itself.
func (m *lifecycleModel) RuleDispatchTheNextQueuedTick(tc hegel.TestCase) {
	if len(m.queue) == 0 || m.stopped {
		tc.Assume(false)
	}
	entry := m.queue[0]
	m.counts["dispatch"]++

	// A11's read site: a unit struck out is held out of dispatch, and only a
	// person releases it.
	if outcome := m.r.MayDispatch(entry.TickID); outcome == Held {
		m.logf("%s is held out of dispatch (A11): not claimed, not started", entry.TickID)
		return
	}
	// The admission decision itself — the width, the wave boundary, the role
	// jobs, the capacity, the adoption path — over the real window.
	if !m.r.mayAdmit(entry, &m.window, m.plan) {
		m.logf("%s is not admitted: the window holds the width, a wave boundary or a role job", entry.TickID)
		return
	}
	// An attempt this run already has is ADOPTED: no new claim, no new start,
	// which is the whole of "a live job is never redispatched" — the #50
	// resume order, and the never_redispatch_live guard's own name.
	if m.liveOf(entry.TickID) {
		m.adopt(entry)
		return
	}
	// The claim: tk's own arithmetic answers, and the window must never have
	// asked for one the width refuses — mayAdmit said it would not. A claim
	// this run already holds (dz1, tick 823) takes no new one. The claim's
	// generation is stamped on the attempt: until the next fresh read of the
	// graph, the width arithmetic can only see it through the window.
	if !entry.OwnClaim && !entry.StaleClaim {
		if _, err := m.tr.Claim(m.ctx, entry.TickID, "ticfac-test"); err != nil {
			// THE RECORDED COUNTEREXAMPLE, not a failure of the model:
			// admission counted fewer claims than the tracker holds, and asked
			// for one more. The shape is recorded and counted, the tick is held
			// (nothing was started, exactly as a refusal to admit would leave
			// it), and the width invariant holds the record to the one known
			// explanation — a parked claim the width's readings cannot see.
			m.counts["over_claim"]++
			explained := m.invisibleParkedClaims() > 0
			m.overClaims = append(m.overClaims, explained)
			m.logf("%s: admission counted %d claims against the width of %d, asked the tracker for one more and was refused%s",
				entry.TickID, m.r.claimsHeld(&m.window, m.plan), m.width,
				map[bool]string{true: " — the recorded counterexample: a parked claim the readings cannot see",
					false: " — WITH NO PARKED CLAIM TO EXPLAIN IT"}[explained])
			return
		}
	}
	// The marker is written on origin BEFORE the job is started, so a
	// reconciler that lost the race is refused by the repository rather than
	// by a lock it might have lost (Appendix A #1).
	attempt := len(m.startsOf(entry.TickID)) + 1
	ref := "marker:" + entry.TickID
	marker := fmt.Sprintf("attempt %d of %s", attempt, entry.TickID)
	if err := m.write("the dispatch marker of "+entry.TickID, ref, m.origin.get(ref), marker); err != nil {
		m.fail("%s: the dispatch marker could not be written durably: %v", entry.TickID, err)
	}
	// The start — the observable the redispatch invariant is made of. The
	// live set it records is the one the start LANDED ON, so the snapshot is
	// taken before this attempt exists: a start over a tick this run already
	// had live is the redispatch the invariant exists to catch.
	start := modelStart{tick: entry.TickID, live: m.liveTicks()}
	m.attempts[entry.TickID] = &modelAttempt{tick: entry.TickID, attempt: attempt, state: "dispatched",
		claimGen: m.readGen}
	m.starts = append(m.starts, start)
	m.window.live = append(m.window.live, &inflightAttempt{entry: entry})
	m.r.noteAlive(entry.TickID)
	m.r.setTick(entry.TickID, "dispatched")
	m.record(entry.TickID, StageDispatched, "%s claimed and started as attempt %d", entry.TickID, attempt)
	m.queue = m.queue[1:]
	m.logf("%s dispatched (attempt %d, wave %d, role %q)", entry.TickID, attempt, entry.Wave, entry.Role)
}

// startsOf is the starts one tick has had, for the attempt numbering.
func (m *lifecycleModel) startsOf(tick string) []modelStart {
	var out []modelStart
	for _, start := range m.starts {
		if start.tick == tick {
			out = append(out, start)
		}
	}
	return out
}

// RuleAWorkerSettles has one live worker answer: succeeded, failed, or with no
// report at all. The attempt leaves the live half whichever way it ends — and
// the poll that observes the settle is the keepalive (A4), so it happens
// through the real Poll.
func (m *lifecycleModel) RuleAWorkerSettles(tc hegel.TestCase) {
	if len(m.window.live) == 0 || m.stopped {
		tc.Assume(false)
	}
	m.counts["settle"]++
	idx := hegel.Draw(tc, hegel.Integers(0, len(m.window.live)-1))
	fl := m.window.live[idx]
	tick := fl.entry.TickID
	// The poll IS the keepalive: an attempt unaddressed past the substrate's
	// threshold is gone, and the reconciler learns that here.
	if m.r.Poll(tick) == Wiped {
		m.logf("%s went unaddressed past the wipe threshold and is gone", tick)
	}
	outcome := m.forceOutcome
	if outcome == "" {
		outcome = hegel.Draw(tc, hegel.SampledFrom([]string{
			subprocess.StateSucceeded, subprocess.StateSucceeded, subprocess.StateSucceeded,
			subprocess.StateSucceeded, subprocess.StateFailed, "no report",
		}))
	}
	m.window.live = append(m.window.live[:idx], m.window.live[idx+1:]...)
	switch outcome {
	case subprocess.StateSucceeded, subprocess.StateFailed:
		state := subprocess.StateSucceeded
		if outcome == subprocess.StateFailed {
			state = subprocess.StateFailed
		}
		m.attempts[tick].state, m.attempts[tick].terminal = "settled", true
		m.window.settled = append(m.window.settled,
			&settledAttempt{fl: fl, status: &subprocess.JobStatus{State: state, Terminal: true}})
		m.r.setTick(tick, "reported")
		m.logf("%s settled %s and waits its turn to be finished", tick, outcome)
	default:
		// No report at all: the missing-result verdict. It left nothing to
		// carry, so the window dispatches the tick again in this run
		// (redispatchesInRun), into the claim it already holds.
		refusal := m.r.refuse(RefusedCollect, tick,
			"%s produced no report at all: nothing was collected, and the attempt left nothing behind", tick)
		refusal.neverAnswered = true
		m.attempts[tick].state, m.attempts[tick].terminal = "rejected", true
		m.r.setTick(tick, refusedTickState(refusal))
		if !redispatchesInRun(refusal) {
			m.fail("%s: the missing-result verdict must be redispatched in-run, and the hold vocabulary says it is not", tick)
		}
		entry := fl.entry
		entry.Claimed, entry.OwnClaim, entry.StaleClaim, entry.InFlight = true, true, false, false
		m.queue = append([]planEntry{entry}, m.queue...)
		m.logf("%s never answered and is dispatched again in this run, into the claim it already holds", tick)
	}
}

// RuleFinishTheSettledAttempt finishes the attempt that settled first, the way
// the window does: collect, integrate, gate, close, and only then clean up.
// Finishing is serial — one at a time — and a finish cut between its gate and
// its close is taken back by the next incarnation, which must re-establish the
// verdict against the tree as it stands before it closes anything.
func (m *lifecycleModel) RuleFinishTheSettledAttempt(tc hegel.TestCase) {
	if m.stopped {
		tc.Assume(false)
	}
	if len(m.window.settled) == 0 {
		tc.Assume(false)
	}
	m.counts["finish"]++
	next := m.window.settled[0]
	m.window.settled = m.window.settled[1:]
	tick := next.fl.entry.TickID

	// A finish cut between its gate and its close (the real suite's third
	// cut): the standing verdict is checked against the tree as it stands —
	// published through the real A13 seam, refused as stale when the tree
	// moved — and only a fresh verdict closes anything.
	if standing := m.gates[tick]; standing != nil && standing.passed && m.r.tickState(tick) == "reported" {
		switch m.r.PublishEvidence(standing.key, m.gateFingerprint()) {
		case "published":
			standing.published = true
			m.logf("%s closes behind the standing gate verdict, still fresh over the tree as it stands", tick)
			m.close(tick, standing)
			return
		case "refused_stale":
			// Mismatch names every field that moved — the real seam's own
			// answer, so the counterexample says what made the verdict stale.
			m.logf("%s: the standing gate verdict is stale against the tree as it stands (%s), so the gate runs again",
				tick, strings.Join(standing.finger.Mismatch(m.gateFingerprint()), "; "))
		default:
			m.logf("%s: no gate evidence stands for it, so the gate runs", tick)
		}
	}

	// The collect: the report is read off the attempt's branch. A worker that
	// failed produced a report that refuses its own work — the merge never
	// happens — so it is rejected here, through the real hold vocabulary.
	if next.status != nil && next.status.State == subprocess.StateFailed {
		refusal := m.r.refuse(RefusedCollect, tick, "%s reported a failure: its work does not merge", tick)
		if !holdsOnlyItsTick(refusal) {
			m.fail("%s: a failed collect must hold only its own tick, and the hold vocabulary says it does not", tick)
		}
		m.park(tick, refusal)
		m.attempts[tick].state = "rejected"
		return
	}
	// The merge: the attempt's work lands on the integration branch. A
	// conflict another tick of the same wave also produced — the merge that
	// cannot be resolved — holds only this tick (epic av8's wave 4).
	if conflict := m.conflictNext || (!m.armAlways && hegel.Draw(tc, hegel.Integers(0, 7)) == 0); conflict {
		m.conflictNext = false
		refusal := m.r.refuse(RefusedMerge, tick,
			"%s does not integrate onto epic/%s: another tick of the same wave wrote the same file", tick, m.epicID())
		refusal.conflict = true
		if !holdsOnlyItsTick(refusal) {
			m.fail("%s: a same-wave merge conflict must hold only its own tick", tick)
		}
		m.park(tick, refusal)
		m.attempts[tick].state = "rejected"
		return
	}
	m.conflictNext = false
	if err := m.write("the integration branch", "branch", m.origin.get("branch"),
		fmt.Sprintf("%s merged (merge %d)", tick, m.counts["merges"]+1)); err != nil {
		m.fail("%s: its merge could not be written durably: %v", tick, err)
	}
	m.counts["merges"]++

	// The gate: the integrated tree is what it runs against, and the evidence
	// is recorded BEFORE the verdict is published (A13). A gate that fails
	// stops the run — its merge is on the branch, so every later gate would
	// fail for a reason that is not its own — and nothing closes behind it.
	if !m.runGate(tick, tc) {
		return
	}
	// The cut a crash (or a step boundary) can genuinely land on: the gate's
	// verdict is DURABLE and the close is not. The attempt stays at the head
	// of the settled half, and the next round of this run — or the next
	// INCARNATION, from the standing evidence alone — re-establishes the
	// verdict's freshness against the tree as it stands before it closes.
	if cut := m.forceCut || (!m.armAlways && hegel.Draw(tc, hegel.WeightedBooleans(0.25))); cut {
		m.forceCut = false
		// The attempt stays at the head of the settled half, its claim held by
		// the window: a finish in progress is a claim in progress (the real
		// window's `finish` holder), so the width keeps counting it.
		m.window.settled = append([]*settledAttempt{next}, m.window.settled...)
		m.logf("the finish of %s is cut between its gate and its close: the verdict is durable, the close is not", tick)
		return
	}
	m.close(tick, m.gates[tick])
}

// runGate runs the integrated gate over the branch as it stands and records
// its verdict through the real A13 seam. It answers whether the gate passed.
func (m *lifecycleModel) runGate(tick string, tc hegel.TestCase) bool {
	key := "gate:" + tick
	fingerprint := m.gateFingerprint()
	if m.r.RecordEvidence(key, fingerprint) == "refused_unfingerprinted" {
		m.fail("%s: the gate's evidence was refused for lacking a fingerprint", tick)
	}
	gate := &modelGate{tick: tick, covered: m.counts["merges"], key: key, finger: fingerprint}
	if failing := m.gateFailsNext || (!m.armAlways && hegel.Draw(tc, hegel.Integers(0, 7)) == 1); failing {
		m.gateFailsNext = false
		gate.passed = false
		m.gates[tick] = gate
		m.record(tick, StageGateFailed, "the integrated gate refused the tree")
		m.logf("the gate FAILED over %d merges: the run stops and nothing closes behind it", m.counts["merges"])
		m.stop(RefusedGate, tick,
			fmt.Sprintf("the integrated gate did not pass over the tree %s merged onto", tick))
		return false
	}
	m.gateFailsNext = false
	gate.passed = true
	// Publication checks freshness: the record is compared against the
	// CURRENT target, and a record about a tree that has moved is refused.
	if m.r.PublishEvidence(key, m.gateFingerprint()) == "refused_stale" {
		m.fail("%s: a gate verdict about the tree as it stands was refused as stale", tick)
	}
	gate.published = true
	m.gates[tick] = gate
	m.record(tick, StageGatePassed, "the integrated gate passed over %d merges", gate.covered)
	m.logf("the gate passed over %d merges", gate.covered)
	return true
}

// gateFingerprint is what the gate evaluates, as the evidence record states it:
// the contract's four fields, plus the branch head and the merge count — the
// two things a resumed incarnation must find unchanged before it trusts a
// standing verdict.
func (m *lifecycleModel) gateFingerprint() Fingerprint {
	return Fingerprint{"source_sha": "sha-base", "integration_ref": "epic/" + m.epicID(),
		"context_manifest_digest": "manifest", "profile_digest": "profile",
		"branch_head": m.origin.get("branch"), "merges": fmt.Sprint(m.counts["merges"])}
}

// close is the one place a tick closes: behind a gate that passed over the tree
// as it stands, published through the real seam. The claim ends here and
// nowhere earlier.
func (m *lifecycleModel) close(tick string, gate *modelGate) {
	if gate == nil || !gate.passed || gate.covered != m.counts["merges"] {
		m.fail("%s closed behind a gate that did not pass over the tree it stands on", tick)
	}
	m.counts["closes"]++
	closed, err := m.tr.Close(m.ctx, tick)
	if err != nil {
		m.fail("%s: the tracker refused the close: %v", tick, err)
	}
	m.closes = append(m.closes, modelClose{tick: tick, merges: m.counts["merges"], gateCovered: gate.covered})
	m.attempts[tick].state = "closed"
	m.attempts[tick].terminal = true
	m.r.setTick(tick, "closed")
	m.record(tick, StageClosed, "%s closed behind a gate that passed over %d merges", closed.ID, gate.covered)
	m.logf("%s closed (the gate covered %d of %d merges)", tick, gate.covered, m.counts["merges"])
}

// park holds one tick out of the run without stopping it (epic hn6's
// run_ee8e3de): a refusal that holds only its own tick is parked, and every
// tick that does not wait behind it keeps working.
func (m *lifecycleModel) park(tick string, refusal *Refusal) {
	m.parked[tick] = true
	if attempt, ok := m.attempts[tick]; ok {
		m.parkedGen[tick] = attempt.claimGen
	}
	m.r.setTick(tick, refusedTickState(refusal))
	if holdsForAPerson(refusal.Reason) {
		m.record(tick, StageRunHeld, "%s: %s", refusal.Reason, refusal.Message)
	} else {
		m.r.recordRefusal(tick, refusal)
	}
	m.logf("%s is held on %s and the run goes on without it", tick, refusal.Reason)
}

// stop is what a refusal that holds the whole run does: the run ends, and the
// attempts it walks away from are announced as abandoned.
func (m *lifecycleModel) stop(reason, tick, message string) {
	refusal := m.r.refuse(reason, tick, "%s", message)
	m.stopped = true
	if holdsForAPerson(reason) {
		m.record(tick, StageRunHeld, "%s: %s", reason, message)
	} else {
		m.r.recordRefusal(tick, refusal)
	}
	m.logf("the run stopped on %s (%s)", reason, tick)
}

// RuleTheReconcilerCrashesAndRestarts kills this incarnation's memory and
// starts the next from the durable records alone: the tracker's graph and
// claims, the attempt markers on origin, the checkpoint states, the gate
// evidence. Nothing else survives — exactly what a restart on a fresh clone
// gets.
func (m *lifecycleModel) RuleTheReconcilerCrashesAndRestarts(tc hegel.TestCase) {
	// A crash is interesting when there is something to lose: an incarnation
	// holding nothing crashes into an identical next one.
	if m.stopped || len(m.window.live)+len(m.window.settled) == 0 {
		tc.Assume(false)
	}
	m.counts["restart"]++
	// The durable truth at the moment of the crash.
	m.restartLive = m.liveTicks()
	m.restartClaim = m.inFlightIDs()

	// The incarnation's memory is gone: the window, the queue, the journal.
	m.window = held{}
	m.r.journal = nil
	m.parked = map[string]bool{}
	m.conflictNext, m.gateFailsNext = false, false

	// A cold start re-derives: the plan from the graph, the claims from the
	// same read, the live attempts from the checkpoint states and the markers
	// — and then ADMISSION makes the window, exactly as it does in the warm
	// run: the adoption-first queue puts every tick this run already has an
	// attempt of at its head, and each one is taken back by identity. A
	// settled attempt nobody closed is finished by the next incarnation from
	// the branch and the report; a dispatched one is adopted live. Both keep
	// their place in the window, which is what makes "no false close" an
	// assertion rather than a hope.
	m.r.inFlightIDs = m.inFlightIDs()
	m.plan = m.planFromTracker()
	m.queue = append([]planEntry{}, m.plan...)
	for _, entry := range append([]planEntry{}, m.queue...) {
		m.adopt(entry)
	}
	m.restarted = true
	m.record("", StageResumed, "a new incarnation re-derived its state from the durable records")
	m.logf("the reconciler crashed and restarted: %d live attempts adopted, %d claims counted",
		len(m.window.live), len(m.r.inFlightIDs))
	// The one-shot check of the re-derivation: what the new incarnation holds
	// is exactly what the durable records said existed at the cut. It cannot
	// be a standing invariant — the run goes on and adopts more — so it is
	// made HERE, where the durable truth and the re-derivation are both in
	// hand.
	adopted := map[string]bool{}
	for _, fl := range m.window.live {
		adopted[fl.entry.TickID] = true
	}
	for _, tick := range m.restartLive {
		if !adopted[tick] {
			m.fail("%s was live at the crash and the restarted incarnation did not adopt it", tick)
		}
	}
	if strings.Join(m.inFlightIDs(), ",") != strings.Join(m.restartClaim, ",") {
		m.fail("the claims changed across the restart: [%s] before, [%s] after — a cold start must reach the same state",
			strings.Join(m.restartClaim, ","), strings.Join(m.inFlightIDs(), ","))
	}
}

// adopt takes back one attempt this run already has, by identity: the marker
// on origin is the attempt's own, so the tick is neither claimed again nor
// started over. A settled attempt goes to the settled half to be finished; a
// dispatched one goes back to the live half. The entry leaves the queue the
// moment it is adopted, which is what keeps one tick out of the window twice.
func (m *lifecycleModel) adopt(entry planEntry) {
	tick := entry.TickID
	attempt, ok := m.attempts[tick]
	if !ok {
		return
	}
	switch {
	case attempt.terminal && attempt.state == "settled":
		m.window.settled = append(m.window.settled, &settledAttempt{
			fl:     &inflightAttempt{entry: entry},
			status: &subprocess.JobStatus{State: subprocess.StateSucceeded, Terminal: true}})
	case !attempt.terminal && attempt.state == "dispatched":
		m.window.live = append(m.window.live, &inflightAttempt{entry: entry})
	default:
		return // terminal work the window already disposed of, or nothing durable
	}
	m.record(tick, StageAdopted, "adopted by identity: the attempt this run left on origin is taken back")
	m.logf("%s was adopted by identity (attempt %d, %s)", tick, attempt.attempt, attempt.state)
	m.queue = dropTick(m.queue, tick)
}

// dropTick removes every entry of one tick from a queue.
func dropTick(queue []planEntry, tick string) []planEntry {
	out := make([]planEntry, 0, len(queue))
	for _, entry := range queue {
		if entry.TickID != tick {
			out = append(out, entry)
		}
	}
	return out
}

// RuleARemoteWriteFailsTransiently arms a transient failure for the next
// durable write: the remote resets the connection, nothing got an answer, and
// the same write goes through when it is asked again. The run must wait
// through it — the real RemoteRetry — and lose nothing.
func (m *lifecycleModel) RuleARemoteWriteFailsTransiently(tc hegel.TestCase) {
	// A remote failure is a fact about a write the run has something to write
	// for: with nothing in flight there is no write to lose.
	// A remote failure is a fact about a write the run has something to write
	// for, and the draw's coin is what keeps it a fault rather than the rule
	// the engine always prefers: half the draws leave the write alone.
	if m.stopped || len(m.window.live)+len(m.window.settled) == 0 ||
		(!m.armAlways && !hegel.Draw(tc, hegel.WeightedBooleans(0.5))) {
		tc.Assume(false)
	}
	m.counts["transient"]++
	before := m.origin.writes
	m.origin.fault = "transient"
	// The next durable write: the run's own checkpoint, written under the
	// same compare-and-swap every other write uses.
	if err := m.write("the checkpoint", "checkpoint", m.origin.get("checkpoint"), "checkpoint 1"); err != nil {
		m.fail("a transient remote failure stopped the run instead of being waited through: %v", err)
	}
	if m.origin.writes != before+1 {
		m.fail("a transient remote failure lost the write: origin moved %d times where one write was owed",
			m.origin.writes-before)
	}
	m.origin.fault = ""
	m.logf("a transient remote failure was waited through and the write landed")
}

// RuleAWriteLosesItsAck arms the lost-acknowledgement shape for the next
// durable write (tick o82, epic-gvc): the write LANDS, the ack never comes
// back, and the writer's retry sees the ref already moved. Only a genuinely
// foreign change is a conflict.
func (m *lifecycleModel) RuleAWriteLosesItsAck(tc hegel.TestCase) {
	// The lost ack is a fact about a write the run is making — the checkpoint
	// of an attempt it is holding — and the draw's coin keeps it a fault
	// rather than the rule the engine always prefers.
	if m.stopped || len(m.window.live)+len(m.window.settled) == 0 ||
		(!m.armAlways && !hegel.Draw(tc, hegel.WeightedBooleans(0.5))) {
		tc.Assume(false)
	}
	m.counts["lost_ack"]++
	before := m.origin.writes
	m.origin.fault = "lost_ack"
	if err := m.write("the checkpoint", "checkpoint", m.origin.get("checkpoint"), "checkpoint 2"); err != nil {
		m.fail("a write whose ack was lost stopped the run instead of being recognised as its own: %v", err)
	}
	if m.origin.writes != before+1 {
		m.fail("a lost-ack write was written %d times where one write was owed", m.origin.writes-before)
	}
	m.origin.fault = ""
	m.logf("a lost ack was recognised as this writer's own and the run continued")
}

// RuleASameWaveMergeConflicts makes the next merge one that cannot be
// resolved: two ticks of one wave wrote the same file, which is the wave the
// composition rule exists to refuse. The refusal holds only its own tick.
func (m *lifecycleModel) RuleASameWaveMergeConflicts(tc hegel.TestCase) {
	// The conflict is drawn only half the time it could stand: the merge that
	// goes through is the run's ordinary path, and the engine must walk it.
	if len(m.window.settled) == 0 || m.stopped ||
		(!m.armAlways && !hegel.Draw(tc, hegel.WeightedBooleans(0.75))) {
		tc.Assume(false)
	}
	m.counts["conflict"]++
	m.conflictNext = true
	m.logf("the next merge will conflict: two ticks of one wave wrote the same file")
}

// RuleTheGateFails makes the next gate run refuse the tree: the merge is
// already on the branch, so nothing may close behind it and the run stops.
func (m *lifecycleModel) RuleTheGateFails(tc hegel.TestCase) {
	// The gate failure is drawn only half the time it could stand: the gate
	// that passes is the run's ordinary path, and the engine must walk it.
	if len(m.window.settled) == 0 || m.stopped ||
		(!m.armAlways && !hegel.Draw(tc, hegel.WeightedBooleans(0.75))) {
		tc.Assume(false)
	}
	m.counts["gate_fails"]++
	m.gateFailsNext = true
	m.logf("the next gate run will refuse the integrated tree")
}

// RuleATickIsAddedMidRun absorbs a finding into the running epic the way a
// person does mid-run (the production shape of tick 3h0): a new tick is created
// under the epic, the fresh graph carries it, and the re-derivation admits it
// — BOUNDED by the absorption depth, past which the finding is deferred to the
// backlog and the run carries on.
func (m *lifecycleModel) RuleATickIsAddedMidRun(tc hegel.TestCase) {
	// An absorption is a fact about a run with work under way: the finding
	// arrives out of an attempt the epic has already settled or closed, which
	// is what a discovery outside a tick's own scope is made of.
	if m.stopped || len(m.queue) == 0 || m.counts["settle"]+m.counts["closes"] == 0 {
		tc.Assume(false)
	}
	m.counts["absorb"]++
	// The chain that produced the reporting tick, the way absorptionChain
	// walks it: each link is a finding an earlier absorption decided and the
	// tick that decision created.
	links := append([]chainLink{}, m.absorbed...)
	bound := DefaultAbsorptionDepthBound
	if absorptionDepthExceeded(links, bound) {
		// Past the bound: a BACKLOG tick, outside the epic, never recursed
		// into — and never work this run dispatches.
		id := fmt.Sprintf("backlog-%d", len(m.trackerState().Order)+1)
		created, err := m.tr.CreateTick(m.ctx, tk.Tick{ID: id, Title: "deferred past the absorption bound",
			Parent: "", CreatedBy: "ticfac run epic-" + m.epicID(), Priority: 2})
		if err != nil {
			m.fail("the backlog tick the bound defers to could not be filed: %v", err)
		}
		m.deferred++
		m.logf("absorbing would be the %dth link of one chain and the bound is %d, so %s is deferred to the backlog",
			len(links)+1, bound, created.ID)
		return
	}
	// Within the bound: a child of the running epic, sequenced before the
	// review, admitted by THIS run rather than by a restart nobody attended.
	id := fmt.Sprintf("a%d", len(m.trackerState().Order)+1)
	created, err := m.tr.CreateTick(m.ctx, tk.Tick{ID: id, Title: "absorbed finding", Parent: m.epicID(), Priority: 1})
	if err != nil {
		m.fail("the absorbed tick could not be created: %v", err)
	}
	if err := m.tr.BlockOn(m.ctx, "rv", created.ID); err != nil {
		m.fail("the absorbed tick could not be sequenced before the review: %v", err)
	}
	m.absorbed = append(m.absorbed, chainLink{
		Finding: runstate.Finding{Key: fmt.Sprintf("key-%d", len(m.absorbed)), TickID: "co",
			Title: "a finding the run absorbed"},
		Record: runstate.Absorption{Key: fmt.Sprintf("key-%d", len(m.absorbed)), TickID: created.ID, Gating: true},
	})
	// The re-derivation: the fresh graph carries the tick the plan does not,
	// and unplannedTicks — the same function replan admits it with — says so.
	// It is a replan moment: the width's readings are refreshed with it, so
	// the reading generation moves on.
	m.readGen++
	graph := m.graph()
	fresh := planFrom(graph)
	added := unplannedTicks(m.plan, fresh)
	if len(added) == 0 {
		m.fail("a tick created under the epic mid-run did not reach the plan through unplannedTicks")
	}
	m.plan = append(m.plan, added...)
	sortPlan(m.plan)
	m.queue = append(m.queue, added...)
	sortPlan(m.queue)
	m.r.inFlightIDs = graph.Dispatch.InFlightIDs
	m.logf("%s was absorbed into the epic at depth %d of at most %d and admitted by this run",
		created.ID, len(m.absorbed), bound)
}

// ------------------------------------------------------- the invariants ---

// InvariantALiveJobIsNeverRedispatched: no start ever happened for a tick
// that already had a live attempt of this run — every start in the history
// stands on a tick nothing of this run was running.
func (m *lifecycleModel) InvariantALiveJobIsNeverRedispatched(_ hegel.TestCase) {
	for _, start := range m.starts {
		for _, live := range start.live {
			if live == start.tick {
				m.fail("%s was started while a live attempt of its own was already running: one tick, two jobs",
					start.tick)
			}
		}
	}
	// The live half agrees with the durable markers: a tick the window holds
	// live is a tick whose attempt is not terminal.
	for _, fl := range m.window.live {
		if !m.liveOf(fl.entry.TickID) {
			m.fail("%s is in the window's live half with no live attempt of this run on origin", fl.entry.TickID)
		}
	}
}

// InvariantNothingClosesBehindAFailingGate: every close stands on a gate that
// passed over the tree as it stood when the close happened — the merge count
// the close records is one the published verdict covered — and the journal's
// order never puts a close before the gate that stands behind it.
func (m *lifecycleModel) InvariantNothingClosesBehindAFailingGate(_ hegel.TestCase) {
	for _, close := range m.closes {
		if close.gateCovered != close.merges {
			m.fail("%s closed behind a gate that covered %d of %d merges: the verdict was not about the tree it stands on",
				close.tick, close.gateCovered, close.merges)
		}
		gate := m.gates[close.tick]
		if gate == nil || !gate.passed || !gate.published {
			m.fail("%s closed behind a gate whose verdict was never published (A13)", close.tick)
		}
		if !m.stageSeen(close.tick, StageGatePassed) {
			m.fail("%s reached %s without a gate having passed for it first", close.tick, StageClosed)
		}
		if !m.stageSeenBefore(close.tick, StageGatePassed, StageClosed) {
			m.fail("%s closed before the gate that stands behind it ran", close.tick)
		}
	}
	if m.stopped {
		for _, settled := range m.window.settled {
			if m.closedIn(settled.fl.entry.TickID) {
				m.fail("%s closed behind the gate failure that stopped the run", settled.fl.entry.TickID)
			}
		}
	}
}

// InvariantTheDeclaredWidthIsNeverExceeded: the tracker's own claim count —
// open claims and the high-water mark — never passes the width, counted the
// way tk counts (a claim lives until its tick closes), and the window's own
// arithmetic never asks for a claim past it.
//
// THE COUNTEREXAMPLE THIS MODEL FOUND, recorded rather than hidden. A tick
// parked by a hold-only-its-tick refusal keeps its claim standing (window.go:
// "a question this run parked keeps its tick's claim standing") and leaves the
// window — so the only places the width arithmetic could count it are the
// graph's dispatch.in_flight_ids and the plan's Claimed flags, and both are
// readings taken at the last replan, which is to say BEFORE the claim was
// taken. The next admission therefore counts fewer claims than the tracker
// holds and asks for one more. Verified at the base in the
// ceilingGate/linger-until shape of hold_test.go's
// TestATicksTerminalRefusalHoldsOnlyThatTick: a1 is tick_held with its claim
// standing, a2 is claimed and worked to the end, and the tracker counts 3
// claims open at once under a declared width of 1 — and the width is 1 by
// DEFAULT, the width every run without an [orchestration] table runs at.
//
// The invariant holds the record to that shape and no more: the tracker
// refuses the over-claim (as tk 0.31 did; production tk 0.32 grants it, which
// is the same defect seen as a peak instead of a refusal), the dispatch
// records it, and every recorded over-claim must be explained by a parked
// claim the readings cannot see. Any other over-claim fails here; fixing the
// finding empties the record and the comment goes with it.
func (m *lifecycleModel) InvariantTheDeclaredWidthIsNeverExceeded(_ hegel.TestCase) {
	if peak := m.tr.peakClaims(); peak > m.width {
		m.fail("%d claims were open at once under a declared width of %d: the window claimed past the width",
			peak, m.width)
	}
	for i, explained := range m.overClaims {
		if !explained {
			m.fail("over-claim %d: admission asked the tracker for a claim past the width with no parked "+
				"claim to explain it — a shape outside the recorded counterexample", i)
		}
	}
	if held := m.r.claimsHeld(&m.window, m.plan); held > m.width {
		m.fail("the window counts %d claims under the epic against a declared width of %d", held, m.width)
	}
	// The width FREES itself only at the close, so a closed tick still reading
	// in_progress in the tracker is a claim the run never ended — the leak
	// that turns "the window is stuck on the width" into a run's way of life.
	state := m.trackerState()
	for id, tick := range state.Ticks {
		if id == state.Epic || tick.Parent != state.Epic || tick.Status != "in_progress" {
			continue
		}
		if m.r.tickState(id) == "closed" {
			m.fail("%s is closed in the checkpoint and still claimed in the tracker: the claim outlived its tick", id)
		}
	}
}

// InvariantAColdRestartReachesTheSameState: the window the run holds is a
// function of the durable records alone — every attempt this run has that is
// neither terminal nor closed is in the window, and nothing the window holds
// exists without a durable attempt behind it — so an incarnation that starts
// cold and re-derives reaches the window the previous one held. The one-shot
// comparison against the pre-crash truth is made in the restart rule, where
// both are in hand; this is the standing form of the same claim.
func (m *lifecycleModel) InvariantAColdRestartReachesTheSameState(_ hegel.TestCase) {
	held := map[string]bool{}
	for _, fl := range m.window.holders() {
		held[fl.entry.TickID] = true
	}
	for _, settled := range m.window.settled {
		held[settled.fl.entry.TickID] = true
	}
	for tick, attempt := range m.attempts {
		if attempt.terminal || attempt.state == "closed" || attempt.state == "rejected" {
			continue
		}
		if !held[tick] {
			m.fail("%s has a live durable attempt (state %s) and no place in the window: an attempt an "+
				"incarnation re-derives from the durable records must be held", tick, attempt.state)
		}
	}
	for tick := range held {
		if _, ok := m.attempts[tick]; !ok {
			m.fail("%s is held by the window with no durable attempt behind it: the window invented work", tick)
		}
	}
}

// InvariantAbsorptionStaysBounded: the chain this run built never carries more
// links than the recorded bound, every finding past it was deferred to the
// backlog, and a deferral never becomes work the run dispatches.
func (m *lifecycleModel) InvariantAbsorptionStaysBounded(_ hegel.TestCase) {
	if len(m.absorbed) > DefaultAbsorptionDepthBound {
		m.fail("the absorption chain carries %d links against a bound of %d", len(m.absorbed), DefaultAbsorptionDepthBound)
	}
	state := m.trackerState()
	for _, id := range state.Order {
		if !strings.HasPrefix(id, "backlog-") {
			continue
		}
		if tick := state.Ticks[id]; tick.Parent == state.Epic {
			m.fail("%s is a backlog deferral and a child of the epic: a past-bound finding became work", id)
		}
	}
	for _, entry := range m.queue {
		if strings.HasPrefix(entry.TickID, "backlog-") {
			m.fail("%s is a past-bound deferral and the queue still carries it as this run's work", entry.TickID)
		}
	}
}

// invisibleParkedClaims counts the claims this run parked that the width's
// arithmetic cannot see: a parked tick's claim stands in the tracker, but the
// arithmetic reads the graph and the plan, and a claim taken since the last
// fresh reading is in neither until the next one. A claim of generation N is
// newer than the reading of generation N — the reading happened first — so
// invisibility is "taken at or after the freshest reading". This is the
// recorded counterexample the width invariant holds to its one shape.
func (m *lifecycleModel) invisibleParkedClaims() int {
	invisible := 0
	for tick, gen := range m.parkedGen {
		if gen >= m.readGen && m.r.tickState(tick) != "closed" {
			invisible++
		}
	}
	return invisible
}

// ------------------------------------------------------------- helpers ---

// stageSeen says whether one tick's feed has reached one stage.
func (m *lifecycleModel) stageSeen(tick, stage string) bool {
	for _, event := range m.feed {
		if event.Tick == tick && event.Stage == stage {
			return true
		}
	}
	return false
}

// stageSeenBefore says whether one tick reached a stage before another.
func (m *lifecycleModel) stageSeenBefore(tick, before, after string) bool {
	for _, event := range m.feed {
		if event.Tick != tick {
			continue
		}
		if event.Stage == after {
			return false
		}
		if event.Stage == before {
			return true
		}
	}
	return false
}

// closedIn says whether one tick has closed in the model's history.
func (m *lifecycleModel) closedIn(tick string) bool {
	for _, close := range m.closes {
		if close.tick == tick {
			return true
		}
	}
	return false
}

// TestPBTTheReconcilerLifecycleHoldsItsInvariants drives the model: every case
// is a fresh epic, a fresh width and an arbitrary sequence of the eight
// operations, with the five invariants checked at every join point.
func TestPBTTheReconcilerLifecycleHoldsItsInvariants(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", tempdir.Pattern("ticfac-lifecycle-pbt-"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	var cases int
	totals := map[string]int{}
	hegel.Test(t, func(ht *hegel.T) {
		dir, err := os.MkdirTemp(root, "case-")
		if err != nil {
			ht.Fatal(err)
		}
		m := newLifecycleModel(t, dir, ht)
		cases++
		hegel.RunStateful(ht, m,
			hegel.WithStatefulStepCount(20),
			hegel.WithAlwaysCheckInvariants(
				"InvariantALiveJobIsNeverRedispatched",
				"InvariantNothingClosesBehindAFailingGate",
				"InvariantTheDeclaredWidthIsNeverExceeded",
				"InvariantAColdRestartReachesTheSameState",
				"InvariantAbsorptionStaysBounded",
			))
		for name, count := range m.counts {
			totals[name] += count
		}
	}, hegel.WithTestCases(55))
	t.Logf("%d cases: %s", cases, countsLine(totals))
	if cases == 0 {
		t.Fatal("the generator drew no cases: the property is vacuous")
	}
	// Not vacuous: the core chain — dispatch, settle, finish, restart — must
	// have run, or the property says nothing about the lifecycle it is about.
	// The faults and the absorption have no floor here: the engine draws them
	// at random and a run that never draws one is a thin run, not a broken
	// one — the walk in this file is their deterministic floor, and the counts
	// above say what this run actually reached.
	for _, name := range []string{"dispatch", "settle", "finish", "restart"} {
		if totals[name] == 0 {
			t.Errorf("the operation %q never ran in %d cases: neither side of its property is exercised", name, cases)
		}
	}
}

// checkInvariants runs the five invariants directly, for the walk: the engine
// calls them at its join points, the walk at its own.
func (m *lifecycleModel) checkInvariants(tc hegel.TestCase) {
	m.InvariantALiveJobIsNeverRedispatched(tc)
	m.InvariantNothingClosesBehindAFailingGate(tc)
	m.InvariantTheDeclaredWidthIsNeverExceeded(tc)
	m.InvariantAColdRestartReachesTheSameState(tc)
	m.InvariantAbsorptionStaysBounded(tc)
}

// TestPBTTheLifecycleModelWalksEveryOperationInOneSequence is the coverage
// floor the property run cannot promise on its own: the engine draws its rules
// at random, and a run that happened never to draw the gate failure would
// leave half of an invariant unexercised. So ONE fixed walk — the shape of the
// real suite's pinned sequences, against the same model — runs every operation
// the tick names, in the order a run would meet them, with all five invariants
// checked after every step. It is deterministic in structure; only the
// worker's answers and the gate's verdicts are drawn.
//
// short: one fixed sequence through the same in-memory model, no git, no processes
func TestPBTTheLifecycleModelWalksEveryOperationInOneSequence(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", tempdir.Pattern("ticfac-lifecycle-pbt-"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hegel.Test(t, func(ht *hegel.T) {
		totals := map[string]int{}
		require := func(what string, ok bool) {
			if !ok {
				ht.Fatalf("the walk cannot run %s: its precondition does not hold", what)
			}
		}
		rule := func(m *lifecycleModel, name string, op func(hegel.TestCase)) {
			before := m.counts[name]
			op(ht)
			if m.counts[name] == before {
				ht.Fatalf("%s ran and counted nothing\nthe machine's log:\n  %s", name, strings.Join(m.log, "\n  "))
			}
			m.checkInvariants(ht)
		}
		// The floor is read from the models' own counts at the end: an
		// operation like a merge or a close is counted inside another rule,
		// and the walk's floor is about the operations the WHOLE walk reached.
		merge := func(models ...*lifecycleModel) {
			for _, m := range models {
				for name, count := range m.counts {
					totals[name] += count
				}
			}
		}

		// WALK ONE: the run's ordinary life, at the width every gate without
		// an [orchestration] table runs at.
		m := newWalkModel(t, root, ht)

		// Work is dispatched; the remote fails transiently under it; a write
		// loses its ack; the reconciler crashes and restarts and ADOPTS the
		// attempt rather than redispatching it.
		require("a dispatch", len(m.queue) > 0)
		rule(m, "dispatch", m.RuleDispatchTheNextQueuedTick)
		require("a live attempt", len(m.window.live) > 0)
		rule(m, "transient", m.RuleARemoteWriteFailsTransiently)
		rule(m, "lost_ack", m.RuleAWriteLosesItsAck)
		rule(m, "restart", m.RuleTheReconcilerCrashesAndRestarts)
		require("an adopted attempt", len(m.window.live) > 0)

		// The worker settles; the finish is CUT between its gate and its
		// close, and the next one takes it up from the standing evidence.
		rule(m, "settle", m.RuleAWorkerSettles)
		m.forceCut = true
		for range 3 {
			if len(m.window.settled) == 0 {
				break
			}
			rule(m, "finish", m.RuleFinishTheSettledAttempt)
		}
		require("a close behind a passing gate", m.counts["closes"] > 0)

		// A tick is absorbed into the running epic mid-run; the next tick
		// settles and its merge conflicts with another of its wave: it is
		// held, and the run goes on without it.
		require("work left to absorb into", len(m.queue) > 0)
		rule(m, "absorb", m.RuleATickIsAddedMidRun)
		rule(m, "dispatch", m.RuleDispatchTheNextQueuedTick)
		rule(m, "settle", m.RuleAWorkerSettles)
		rule(m, "conflict", m.RuleASameWaveMergeConflicts)
		require("the conflicted attempt to finish", len(m.window.settled) > 0)
		rule(m, "finish", m.RuleFinishTheSettledAttempt)
		require("a tick held on the merge conflict", len(m.parked) > 0)

		// The width's recorded counterexample: the parked claim is invisible
		// to the width's readings, so the next admission asks the tracker for
		// a claim past the width. The model records it; the invariant holds
		// the record to this one shape.
		require("work left to admit", len(m.queue) > 0)
		rule(m, "dispatch", m.RuleDispatchTheNextQueuedTick)
		require("the recorded over-claim", m.counts["over_claim"] > 0)

		// WALK TWO: the gate failure, on a fresh world — a failing gate
		// STOPS the run, so it is the last operation of a run of its own.
		g := newWalkModel(t, root, ht)
		rule(g, "dispatch", g.RuleDispatchTheNextQueuedTick)
		require("a live attempt whose gate will fail", len(g.window.live) > 0)
		rule(g, "settle", g.RuleAWorkerSettles)
		require("a settled attempt to finish", len(g.window.settled) > 0)
		rule(g, "gate_fails", g.RuleTheGateFails)
		rule(g, "finish", g.RuleFinishTheSettledAttempt)
		require("the run stopped on the gate", g.stopped)
		require("nothing closed behind the failing gate", g.counts["closes"] == 0)

		merge(m, g)
		for _, name := range []string{"dispatch", "settle", "finish", "restart", "transient", "lost_ack",
			"conflict", "gate_fails", "absorb", "merges", "closes", "over_claim"} {
			if totals[name] == 0 {
				ht.Fatalf("the walk never ran %s: the coverage floor is a bug in the walk, not a draw", name)
			}
		}
	}, hegel.WithTestCases(1))
}

// newWalkModel builds the walk's world: the faults armed on request, the
// worker's answer fixed, and the width one — the width every gate without an
// [orchestration] table runs at, and the width at which the recorded
// over-claim is deterministic rather than a draw.
func newWalkModel(t *testing.T, root string, tc hegel.TestCase) *lifecycleModel {
	t.Helper()
	dir, err := os.MkdirTemp(root, "walk-")
	if err != nil {
		t.Fatal(err)
	}
	m := newLifecycleModel(t, dir, tc)
	m.armAlways, m.forceOutcome = true, subprocess.StateSucceeded
	m.width, m.r.hostWidth = 1, 1
	m.tr.refuseClaimsBeyond(1)
	return m
}

// countsLine renders the coverage floor's numbers.
func countsLine(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return strings.Join(parts, " ")
}
