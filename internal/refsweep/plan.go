// Package refsweep retires the refs of ENDED runs from origin (tick 6is).
//
// THE PROBLEM. Every run leaves refs on origin: its job branches
// (ticfac/run-<run>/...), the start commits it published for cloud workers
// (refs/ticfac/start/run-<run>/...), its wip snapshots
// (refs/ticfac/wip/run-<run>/...), and — for the cloud executor — one
// landing branch per attempt (tick/<epic>/attempt-<n>/<tick>) beside a
// boot-stopped marker when the container stopped in its boot. The local
// leftover sweep (internal/reconcile/sweep.go) removes a job's LOCAL branch,
// and since tyv a closed tick's wip refs on origin, but nothing ever removed
// the rest: measured 2026-09-30, 205 of origin's 281 heads were job branches
// of 14 runs.
//
// THE RULE. A ref is retired only when it is one of ticfac's OWN names
// (Classify) and the run it belongs to has ENDED:
//
//   - the run completed (its checkpoint says so: the epic was merged or is
//     a ready PR), or
//   - its epic is closed, or
//   - a later run of the same epic exists (superseded), and this run has
//     said nothing for LiveWindow — a run that is merely stopped stays
//     resumable by id and keeps everything while it is the epic's newest.
//
// A landing branch (tick/<epic>/attempt-*) names its epic, not its run: it
// is retired when its epic is closed or every run of the epic has ended.
//
// An ended run's ref is then deleted when it holds nothing the history does
// not — its head is reachable from the base branch or the epic's
// integration branch (merged, or never carrying a commit past its base) —
// or when it is a boot-stopped marker (a diagnostic, never work: the run's
// feed carries its reason). A ref holding commits nothing merged is KEPT
// until Grace after its epic closed; then it goes too.
//
// Everything else is never touched: epic/* branches, main, tags, and every
// name ticfac does not own (a person's branch). Deletes go by exact ref name
// with a lease on the sha the verdict was made on, never by pattern.
package refsweep

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// DefaultGrace is how long an ended run's unmerged ref outlives its epic's
// close: long enough for a person to look at a rejected attempt the feed
// names.
const DefaultGrace = 14 * 24 * time.Hour

// DefaultLiveWindow is how recently a superseded run must have said
// something for it to count as possibly live still. A run whose checkpoint
// moved within it keeps its refs even when a newer run of its epic exists.
const DefaultLiveWindow = 6 * time.Hour

// The families of ref ticfac owns, as the report counts them.
const (
	FamilyRunBranch  = "ticfac/run-*"
	FamilyStart      = "refs/ticfac/start/run-*"
	FamilyWip        = "refs/ticfac/wip/run-*"
	FamilyLanding    = "tick/<epic>/attempt-*"
	FamilyBootMarker = "*-boot-stopped"
)

// BootStoppedSuffix is the marker branch's suffix
// (sandboximage.WorkerBootStoppedBranch).
const BootStoppedSuffix = "-boot-stopped"

// Owned is what one ref name says about itself.
type Owned struct {
	Family string
	// Run is the run id the name is keyed by ("" for a landing branch).
	Run string
	// Epic is the epic id a landing branch is keyed by ("" for a run's ref).
	Epic string
}

// Classify answers whether ref (a full refname) is one of ticfac's own run
// refs, and which. Anything else — epic/*, main, tags, a person's branch, a
// tick/ branch that is not an attempt's landing branch — is not.
func Classify(ref string) (Owned, bool) {
	var o Owned
	var ok bool
	switch {
	case strings.HasPrefix(ref, "refs/heads/ticfac/run-"):
		o.Family = FamilyRunBranch
		o.Run, ok = runSegment(strings.TrimPrefix(ref, "refs/heads/ticfac/run-"))
	case strings.HasPrefix(ref, "refs/ticfac/start/run-"):
		o.Family = FamilyStart
		o.Run, ok = runSegment(strings.TrimPrefix(ref, "refs/ticfac/start/run-"))
	case strings.HasPrefix(ref, "refs/ticfac/wip/run-"):
		o.Family = FamilyWip
		o.Run, ok = runSegment(strings.TrimPrefix(ref, "refs/ticfac/wip/run-"))
	case strings.HasPrefix(ref, "refs/heads/tick/"):
		// tick/<epic>/attempt-<n>[-<job>]/<tick>[-boot-stopped]
		parts := strings.Split(strings.TrimPrefix(ref, "refs/heads/tick/"), "/")
		if len(parts) == 3 && parts[0] != "" && parts[2] != "" && isAttemptSlot(parts[1]) {
			o.Family, o.Epic, ok = FamilyLanding, parts[0], true
		}
	}
	if ok && strings.HasSuffix(ref, BootStoppedSuffix) {
		o.Family = FamilyBootMarker
	}
	return o, ok
}

// runSegment splits "<run>/<rest>" and refuses a name with no run or no rest:
// a ref that IS the namespace root is not a job's.
func runSegment(s string) (string, bool) {
	run, rest, ok := strings.Cut(s, "/")
	if !ok || run == "" || rest == "" {
		return "", false
	}
	return run, true
}

// isAttemptSlot is "attempt-<n>" or "attempt-<n>-<job>".
func isAttemptSlot(s string) bool {
	rest, ok := strings.CutPrefix(s, "attempt-")
	if !ok || rest == "" || rest[0] < '0' || rest[0] > '9' {
		return false
	}
	return true
}

// Ref is one ref on the remote and the sha it pointed at when listed.
type Ref struct {
	Name string `json:"ref"`
	SHA  string `json:"sha"`
}

// Run is what the run's own checkpoint says.
type Run struct {
	ID        string
	Epic      string
	State     runstate.State
	UpdatedAt time.Time
}

// Epic is what the tracker says about one epic. Known false means the
// tracker had no answer: the epic counts as open.
type Epic struct {
	Known    bool
	Closed   bool
	ClosedAt time.Time
}

// Inputs is everything one plan is made from.
type Inputs struct {
	Refs  []Ref
	Runs  map[string]Run
	Epics map[string]Epic
	// Merged answers whether sha is reachable from the base branch or the
	// named epic's integration branch.
	Merged     func(sha, epic string) bool
	Now        time.Time
	Grace      time.Duration
	LiveWindow time.Duration
}

// Verdict is the plan's answer for one ticfac-owned ref.
type Verdict struct {
	Ref
	Family string `json:"family"`
	Run    string `json:"run,omitempty"`
	Epic   string `json:"epic,omitempty"`
	Delete bool   `json:"delete"`
	Why    string `json:"why"`
}

// Plan judges every ticfac-owned ref in in.Refs; refs ticfac does not own
// get no verdict at all. The verdicts are sorted by ref name.
func Plan(in Inputs) []Verdict {
	if in.Grace == 0 {
		in.Grace = DefaultGrace
	}
	if in.LiveWindow == 0 {
		in.LiveWindow = DefaultLiveWindow
	}
	byEpic := map[string][]Run{}
	for _, run := range in.Runs {
		if run.Epic != "" {
			byEpic[run.Epic] = append(byEpic[run.Epic], run)
		}
	}
	var out []Verdict
	for _, ref := range in.Refs {
		owned, ok := Classify(ref.Name)
		if !ok {
			continue
		}
		v := Verdict{Ref: ref, Family: owned.Family, Run: owned.Run, Epic: owned.Epic}
		var ended bool
		var why string
		if owned.Run != "" {
			v.Epic, ended, why = in.runEnded(owned.Run, byEpic)
		} else {
			ended, why = in.epicEnded(owned.Epic, byEpic)
		}
		if !ended {
			v.Why = why
			out = append(out, v)
			continue
		}
		v.Delete, v.Why = in.retire(ref, owned.Family, v.Epic, why)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// runEnded answers the run's epic and whether the run has ended, with why.
func (in Inputs) runEnded(id string, byEpic map[string][]Run) (string, bool, string) {
	run, ok := in.Runs[id]
	if !ok {
		// No checkpoint anywhere: a local run's id still names its epic, and
		// a closed epic ends every run of it. Anything else is unknown and
		// is kept.
		epic, named := strings.CutPrefix(id, "epic-")
		if named && in.Epics[epic].Closed {
			return epic, true, "its epic " + epic + " is closed"
		}
		return epic, false, "kept: no checkpoint of run " + id + " was found, so it is not known to have ended"
	}
	if e := in.Epics[run.Epic]; e.Closed {
		return run.Epic, true, "its epic " + run.Epic + " is closed"
	}
	if run.State == runstate.StateCompleted {
		return run.Epic, true, "run " + id + " completed"
	}
	if newer := newerRun(run, byEpic[run.Epic]); newer != "" {
		if in.Now.Sub(run.UpdatedAt) < in.LiveWindow {
			return run.Epic, false, fmt.Sprintf("kept: run %s is superseded by %s but said something %s ago, "+
				"within the %s it may still be live in", id, newer, in.Now.Sub(run.UpdatedAt).Round(time.Minute), in.LiveWindow)
		}
		return run.Epic, true, "run " + id + " is superseded by the later run " + newer
	}
	return run.Epic, false, fmt.Sprintf("kept: run %s (%s) is the newest run of open epic %s and may be resumed",
		id, run.State, run.Epic)
}

// newerRun is a run of the same epic whose checkpoint moved after run's, or "".
func newerRun(run Run, of []Run) string {
	newest := ""
	var at time.Time
	for _, other := range of {
		if other.ID != run.ID && other.UpdatedAt.After(run.UpdatedAt) && other.UpdatedAt.After(at) {
			newest, at = other.ID, other.UpdatedAt
		}
	}
	return newest
}

// epicEnded answers whether a landing branch's epic has no run left that
// could come back for it.
func (in Inputs) epicEnded(epic string, byEpic map[string][]Run) (bool, string) {
	if in.Epics[epic].Closed {
		return true, "its epic " + epic + " is closed"
	}
	runs := byEpic[epic]
	if len(runs) == 0 {
		return false, "kept: no run of open epic " + epic + " is known, so its runs are not known to have ended"
	}
	for _, run := range runs {
		if _, ended, why := in.runEnded(run.ID, byEpic); !ended {
			return false, "kept: " + strings.TrimPrefix(why, "kept: ")
		}
	}
	return true, "every run of epic " + epic + " has ended"
}

// retire decides an ENDED run's ref: gone when it holds nothing the history
// does not, or is a marker; otherwise kept until the grace after its epic's
// close.
func (in Inputs) retire(ref Ref, family, epic, ended string) (bool, string) {
	if family == FamilyBootMarker {
		return true, ended + "; a boot-stopped marker carries no work"
	}
	if in.Merged != nil && in.Merged(ref.SHA, epic) {
		return true, ended + "; its head is on the base or integration branch"
	}
	e := in.Epics[epic]
	if !e.Closed || e.ClosedAt.IsZero() {
		return false, "kept: " + ended + ", but it holds commits nothing merged; it is kept until " +
			graceWords(in.Grace) + " after its epic closes"
	}
	until := e.ClosedAt.Add(in.Grace)
	if in.Now.Before(until) {
		return false, "kept: " + ended + ", but it holds commits nothing merged; it is kept until " +
			until.UTC().Format(time.RFC3339)
	}
	return true, ended + "; it holds commits nothing merged, and the grace after its epic closed ended " +
		until.UTC().Format(time.RFC3339)
}

// graceWords spells a grace in days when it is whole days.
func graceWords(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return d.String()
}
