package cli

// The bare-invocation overview (tick 2qz): `ticfac` with no arguments is
// the one screen an unattended factory is glanced at with — every run this
// machine knows, the local runs the machine's registry and the checkout
// name and the factory's cloud runs, attention first.
//
// It is BUILT ON the status model (tick 6dh), not beside it: every run's
// entry carries the same versioned model `ticfac status --json <run>` emits,
// built by the same gathering, and the overview adds only the listing's own
// half — which runs exist (enumeration, a question the per-run model cannot
// answer), the attention-first order, and the one line a person reads: the
// state word, the reason, and the single command that clears a stop. What a
// run is held or failed BY is the model's own wait: the reason and the
// unblocking command come from [statusmodel], never from a second opinion
// the overview would have to keep in agreement with the first.
//
// The exit code is the listing's own: 0 it answered — attention is data the
// screen orders by, not a failure of the command that reports it; a source
// that cannot be read (an unconfigured factory, an unreadable runs
// directory) is named in the overview's own degraded list and in a closing
// prose note, never a refusal: the questions an unattended factory is glanced
// at with are ordered precisely so that "does anything need me" survives a
// factory that cannot be asked.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The overview document's own version, `ticfac.overview.v1`'s schema
// version. The per-run entries carry the status model's own version
// independently: this number moves only with the OVERVIEW's shape (the
// wrapper and its entry fields), never with the model it wraps. v2 added
// the row's `project` (tick nyi): an agent reading the listing must be able
// to see which of the factory's runs are this checkout's and which are
// another project's — the rows the entry-level commands can act on and the
// rows they cannot. v3 added the row's `history`, `history_reason` and
// `started_at`: the listing stays complete, and says which rows are history
// the human view collapses (see markOverviewHistory).
const overviewSchemaVersion = 3

// overviewHistoryAge is how long ago a run must have finished — failed, done
// or cancelled — for the human view to call it history even when nothing
// newer answers it. A week: an unattended factory is glanced at daily, so a
// stop a person has had a full week of glances to act on and has not is
// either abandoned or already answered some other way (an epic resubmitted
// under a new run, an epic closed by hand), and the two rules that catch
// those answer it more precisely than any window. The window only has to be
// long enough that nothing still waiting on a person ages out of sight over
// a weekend — and short enough that a factory's months of history do not
// bury today's one failure. --all and --json still list every run.
const overviewHistoryAge = 7 * 24 * time.Hour

// The overview's closed state vocabulary — the four answers the acceptance
// names, plus the run's own terminal word for a stop that was deliberate:
//
//   - held: the run is stopped holding something only a person can move —
//     a held attempt, an untriaged finding beside a dead run, a run whose
//     process is gone without its own terminal record, or a completed run's
//     open PR (the merge, which is a person's by design);
//   - failed: the run ended in its own failure and nothing holds for a
//     person — the clearing command is the resume after a fix;
//   - running: a live incarnation is working;
//   - done: the run's own work is finished;
//   - cancelled: the run was stopped deliberately — terminal like done, and
//     named as its own word so it never reads as a success it was not.
const (
	overviewStateHeld      = "held"
	overviewStateFailed    = "failed"
	overviewStateRunning   = "running"
	overviewStateDone      = "done"
	overviewStateCancelled = "cancelled"
)

// overviewModel is the --json surface's answer: one versioned document, one
// entry per run, attention first — the same order the prose renders.
type overviewModel struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Degraded      []string      `json:"degraded"`
	Runs          []overviewRun `json:"runs"`
}

// overviewRun is one run's row: the listing's own half (run id, host, epic,
// the state word, the reason, the one clearing command, and — for a cloud
// run — the GitHub project the factory holds it under) beside the FULL
// status model the row renders from — the same object `ticfac status --json
// <run>` emits, so the prose line and the JSON entry cannot drift.
type overviewRun struct {
	RunID     string  `json:"run_id"`
	Host      string  `json:"host"`
	EpicID    string  `json:"epic_id"`
	Project   string  `json:"project,omitempty"`
	State     string  `json:"state"`
	Reason    string  `json:"reason"`
	ClearWith *string `json:"clear_with"`
	// StartedAt is when the run started (a cloud run's record) or claimed
	// its checkout (a local run's registration), RFC3339 — what orders two
	// runs of one epic. Omitted when neither source states it.
	StartedAt string `json:"started_at,omitempty"`
	// History says the human view collapses this row into its summary line:
	// the run needs nobody, and HistoryReason says why — its epic is closed,
	// a later run of the epic answers it, or it finished long ago.
	History       bool              `json:"history"`
	HistoryReason string            `json:"history_reason,omitempty"`
	Model         statusmodel.Model `json:"model"`

	// ours is whether this checkout can claim the run (tick nyi): only then
	// is the epic's tracker state this repo's to read.
	ours bool
	// finishedAt is when the run stopped, when a source states it.
	finishedAt time.Time
}

// overviewCommand is the bare `ticfac`: list every run this machine knows,
// attention first. Local runs are named by the machine's run registry
// (internal/runregistry, tick aj9) beside the checkout's own run directories
// — a LIVE run commits its durable state by plumbing to epic/<id> and never
// touches a working tree, so the checkout this listing runs in may hold
// nothing of it and the registry is what names it — and each run is probed
// and gathered IN THE REPO ITS REGISTRATION NAMES (tick 9ss): liveness is
// per-checkout, so a run live in another checkout reads live here, never
// dead from a probe taken in a checkout that was never the run's. Cloud
// runs are the factory's run index. Every run's answer is the 6dh model,
// gathered by the same code `status --json` gathers with, so the two
// surfaces cannot disagree about one run.
func overviewCommand(ctx context.Context, repo string, asJSON, all bool, stdout, stderr io.Writer) int {
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac: %v\n", err)
			return exitGeneric
		}
		repo = wd
	}
	now := time.Now()
	degraded := []string{}
	runs := []overviewRun{}

	// The local runs: every run this machine knows, each answered by the
	// same probe and gathering one-shot status uses. The machine's run
	// registry is the enumeration's first half — a live run's records never
	// appear in any working tree, so without it the listing names only the
	// merged epics whose run directories a checkout on main holds. The
	// checkout's own run directories ride beside them, and every run is
	// probed IN THE REPO ITS REGISTRATION NAMES.
	regs, registryErr := runregistry.List()
	treeIDs, err := localRunIDs(repo)
	if registryErr != nil || err != nil {
		// A source that cannot be read costs the listing its local half's
		// answer from that source and is named — never a refusal over the
		// other local source or the cloud half it can still answer.
		degraded = append(degraded, "runs")
	}
	seen := map[string]bool{}
	registeredAt := map[string]string{}
	for _, reg := range regs {
		if reg.RunID != "" && !seen[reg.RunID] {
			seen[reg.RunID] = true
			registeredAt[reg.RunID] = reg.RegisteredAt
		}
	}
	ids := make([]string, 0, len(seen)+len(treeIDs))
	for runID := range seen {
		ids = append(ids, runID)
	}
	for _, runID := range treeIDs {
		if !seen[runID] {
			seen[runID] = true
			ids = append(ids, runID)
		}
	}
	sort.Strings(ids)
	for _, runID := range ids {
		// The probing convention (runregistry.WorkingRepo): the registered
		// repo when the machine names one, else this checkout. The probe AND
		// the gathering both go there — a run's pidfile, feed and standing
		// worktrees are per-checkout facts that live in the repo the run
		// works in, and its records are read from that repo's origin view.
		workingRepo, _ := runregistry.WorkingRepo(runID, repo)
		probe := runlife.Probe(workingRepo, runID, now)
		model := localStatusModel(ctx, workingRepo, runID, probe, modelGatherers{graph: epicGraph, ci: statusCI})
		// A local run is this checkout's own by construction.
		entry := overviewEntryOf(model, true)
		entry.StartedAt = registeredAt[runID]
		entry.finishedAt = lastEventTime(model)
		runs = append(runs, entry)
	}

	// The cloud runs: the factory's run index, the same window a truncated
	// run id resolves against. A factory that cannot be asked is a fact the
	// reader needs, not a failure of the local half that already answered.
	cloudNote := ""
	if client, err := newCloudClient(); err != nil {
		degraded = append(degraded, "cloud")
		cloudNote = err.Error()
	} else if data, err := client.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/runs?limit=%d", cloudRunIndexLimit), nil); err != nil {
		degraded = append(degraded, "cloud")
		cloudNote = err.Error()
	} else {
		var response cloudStatusResponse
		if err := decodeCloudJSON(data, &response); err != nil {
			degraded = append(degraded, "cloud")
			cloudNote = err.Error()
		} else {
			// The project this checkout mirrors (tick nyi): the factory may
			// host several projects' runs, and every row below is listed — the
			// glance is the factory's — but this repo's records, tracker and PR
			// are read ONLY for a run this project can claim. Another project's
			// run holds none of this repo's attention: its rows say what its
			// own record and feed say, and name no command this checkout could
			// not run for it.
			repoProject, _ := cloudProjectOf(repo)
			for _, record := range response.Runs {
				ours := cloudRecordBelongsToRepo(repoProject, record.Project)
				liveness := cloudRunLiveness(ctx, record.RunID, record.State)
				model := cloudStatusModel(ctx, client, repo, record.RunID, record, liveness, stderr,
					modelGatherers{graph: epicGraph, ci: statusCI}, ours)
				entry := overviewEntryOf(model, ours)
				entry.Project = strings.TrimSpace(record.Project)
				entry.StartedAt = strings.TrimSpace(record.StartedAt)
				entry.finishedAt = parseRFC3339(record.EndedAt)
				if entry.finishedAt.IsZero() {
					entry.finishedAt = lastEventTime(model)
				}
				runs = append(runs, entry)
			}
		}
	}

	// Which rows are history: the listing stays complete, the human view
	// collapses these into one line (markOverviewHistory).
	markOverviewHistory(ctx, repo, runs, now)

	// Attention first: a stable sort over the state ranks, so the runs keep
	// their enumeration order within each band (local before cloud, the
	// checkout's own order before the factory's) — and every history row
	// after every row that is not.
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].History != runs[j].History {
			return !runs[i].History
		}
		return overviewRank(runs[i].State) < overviewRank(runs[j].State)
	})

	doc := overviewModel{
		SchemaVersion: overviewSchemaVersion,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Degraded:      degraded,
		Runs:          runs,
	}
	if asJSON {
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "ticfac: %v\n", err)
			return exitGeneric
		}
		fmt.Fprintf(stdout, "%s\n", raw)
	} else {
		renderOverview(stdout, doc, cloudNote, all)
	}
	return exitSuccess
}

// overviewEpicClosed says whether an epic is closed in this checkout's
// tracker; the second return says whether the tracker could answer at all.
// Swappable so a test fakes the tracker without a binary, like epicGraph.
var overviewEpicClosed = func(ctx context.Context, repo, epicID string) (closed, known bool) {
	if epicID == "" {
		return false, false
	}
	client, err := tk.NewContext(ctx, tk.Options{Dir: repo})
	if err != nil {
		return false, false
	}
	tick, err := client.Show(ctx, epicID)
	if err != nil {
		return false, false
	}
	return strings.EqualFold(strings.TrimSpace(tick.Status), "closed"), true
}

// markOverviewHistory flags the rows the human view collapses: runs that need
// nobody, however their own records ended. A live run is never history. Any
// other run is history when
//
//   - its epic is closed in this checkout's tracker — asked only for a run
//     this checkout can claim, since another project's epic ids are not this
//     tracker's: the epic is done, and the run's stop is part of how it got
//     there, not something to clear;
//   - a later run of the same epic exists — the later run is the epic's
//     state, and the earlier one's stop was answered by the resubmission
//     (compared only where both runs state when they started, and within one
//     project: another project's epic of the same id is another epic);
//   - or it FINISHED (failed, done, cancelled — never held) more than
//     overviewHistoryAge ago.
//
// A held run of an open epic that no later run answers stays in view however
// old it is: it is still waiting on a person.
func markOverviewHistory(ctx context.Context, repo string, runs []overviewRun, now time.Time) {
	scope := func(run overviewRun) string {
		if run.ours {
			return "\x00" + run.EpicID
		}
		return run.Project + "\x00" + run.EpicID
	}
	latest := map[string]overviewRun{}
	for _, run := range runs {
		started := parseRFC3339(run.StartedAt)
		if run.EpicID == "" || started.IsZero() {
			continue
		}
		key := scope(run)
		if best, ok := latest[key]; !ok || started.After(parseRFC3339(best.StartedAt)) {
			latest[key] = run
		}
	}
	closed := map[string]bool{}
	asked := map[string]bool{}
	for i := range runs {
		run := &runs[i]
		if run.State == overviewStateRunning {
			continue
		}
		if run.ours && run.EpicID != "" {
			if !asked[run.EpicID] {
				asked[run.EpicID] = true
				isClosed, known := overviewEpicClosed(ctx, repo, run.EpicID)
				closed[run.EpicID] = known && isClosed
			}
			if closed[run.EpicID] {
				run.History = true
				run.HistoryReason = fmt.Sprintf("epic %s is closed", run.EpicID)
				continue
			}
		}
		if started := parseRFC3339(run.StartedAt); run.EpicID != "" && !started.IsZero() {
			if best, ok := latest[scope(*run)]; ok && best.RunID != run.RunID &&
				parseRFC3339(best.StartedAt).After(started) {
				run.History = true
				run.HistoryReason = fmt.Sprintf("superseded by %s, a later run of epic %s", best.RunID, run.EpicID)
				continue
			}
		}
		switch run.State {
		case overviewStateFailed, overviewStateDone, overviewStateCancelled:
			if !run.finishedAt.IsZero() && now.Sub(run.finishedAt) > overviewHistoryAge {
				run.History = true
				run.HistoryReason = fmt.Sprintf("finished %d days ago", int(now.Sub(run.finishedAt).Hours()/24))
			}
		}
	}
}

// parseRFC3339 is a timestamp a record states, or the zero time for one it
// does not (or states unreadably) — a time that is not stated orders nothing.
func parseRFC3339(value string) time.Time {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return at
}

// lastEventTime is when the run last said anything, from its own feed.
func lastEventTime(model statusmodel.Model) time.Time {
	if model.Liveness.LastEvent == nil {
		return time.Time{}
	}
	return parseRFC3339(model.Liveness.LastEvent.At)
}

// localRunIDs names the runs whose directories THIS CHECKOUT holds: the
// run directories under `.ticfac/runs/`, which is the durable record a run
// commits to its integration branch — so a checkout on main holds the
// merged epics' records. It is the listing's local half's SECOND source
// (tick 9ss): a LIVE run's records live on epic/<id> and never in a working
// tree, so the machine's run registry is what names it, and this enumeration
// covers only what the checkout's own tree holds. A checkout that holds
// none answers none — the honest empty, not an error.
func localRunIDs(repo string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repo, runstate.Root, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// overviewEntryOf classifies one run's model into the listing's row. The
// classification is a read of the model, nothing else: the waits and
// attention it holds, its liveness, its lifecycle phase — never a second
// gathering of the model's own facts.
//
// A run this checkout cannot claim (ours=false, another project's cloud
// run) names no clearing command (tick nyi): "ticfac run-epic <epic>" and
// "ticfac triage <epic>" are commands THIS repo would run against ITS OWN
// records for the epic id — the one thing a row for another project's run
// must never suggest. The reason stands, the state word stands; the command
// is the one field that lies.
func overviewEntryOf(model statusmodel.Model, ours bool) overviewRun {
	entry := overviewRun{RunID: model.RunID, Host: model.Host, EpicID: model.EpicID, Model: model, ours: ours}

	// Held: anything the model says needs a person. The primary is the
	// hardest stop first — the run's own hold line before a finding, a
	// finding before a dead run, a dead run before the merge — because the
	// one command a line may carry is the one that moves the FIRST thing.
	if primary := primaryAttention(model.Attention); primary != nil {
		entry.State = overviewStateHeld
		entry.Reason = primary.What
		entry.ClearWith = primary.UnblockCommand
		if !ours {
			// Another project's run: whatever the attention says, the command
			// that moves it runs against THIS repo's records — not the
			// command to suggest.
			entry.ClearWith = nil
		}
		return entry
	}
	// A live incarnation is working, whatever its records last said: the
	// resume a person has not seen the end of yet.
	if model.Liveness.Alive {
		entry.State = overviewStateRunning
		entry.Reason = runningReasonOf(model)
		return entry
	}
	switch model.Lifecycle.Phase {
	case statusmodel.PhaseFailed:
		// The run ended in its own failure, holding nothing: the reason is
		// its own last word, and the clearing command is the resume after a
		// fix — named by the host the run lives on (tick gtk), so a cloud
		// failure's resume is a new submission to its factory, never a
		// local foreground restart.
		entry.State = overviewStateFailed
		entry.Reason = lastWordOf(model)
		clear := statusmodel.ResumeCommand(model.Host, model.EpicID)
		entry.ClearWith = &clear
	case statusmodel.PhaseCancelled:
		entry.State = overviewStateCancelled
		entry.Reason = lastWordOf(model)
	default:
		entry.State = overviewStateDone
		entry.Reason = lastWordOf(model)
	}
	// A cloud run's own record state rides as the liveness answer's state,
	// and it is the run's own durable terminal word — the one this checkout
	// may hold NOTHING else of, because the factory hosts other projects'
	// runs whose records never land here. The row says that word, never a
	// phase the missing records cannot state: a finished cloud run is done,
	// a stopped one is cancelled, a failed one failed with the resume.
	switch model.Liveness.State {
	case "failed":
		entry.State = overviewStateFailed
		entry.Reason = cloudEndReasonOf(model)
		clear := statusmodel.ResumeCommand(model.Host, model.EpicID)
		entry.ClearWith = &clear
	case "stopped":
		entry.State = overviewStateCancelled
		entry.Reason = cloudEndReasonOf(model)
	case "completed":
		entry.State = overviewStateDone
		entry.Reason = cloudEndReasonOf(model)
	}
	if !ours {
		entry.ClearWith = nil
	}
	return entry
}

// primaryAttention picks the attention entry a held run's line answers
// with, by the hardness of what it stops: the run's own hold first, then an
// untriaged finding, then a dead run, then the merge. An unknown kind — a
// vocabulary the model grows later — is honest last, never dropped.
func primaryAttention(entries []statusmodel.Attention) *statusmodel.Attention {
	if len(entries) == 0 {
		return nil
	}
	best := &entries[0]
	for i := range entries {
		if attentionRank(entries[i].Kind) < attentionRank(best.Kind) {
			best = &entries[i]
		}
	}
	return best
}

// attentionRank orders the person-needing waits by what a glance should
// clear first.
func attentionRank(kind string) int {
	switch kind {
	case statusmodel.WaitHeldForPerson:
		return 0
	case statusmodel.WaitFinding:
		return 1
	case statusmodel.WaitDeadRun:
		return 2
	case statusmodel.WaitMerge:
		return 3
	}
	return 4
}

// runningReasonOf says what a live run is doing, from the model's own
// answers in their order: what it waits on when that is its own work (the
// in-flight attempts, the close-out's CI), the wave it is in, the phase it
// is past those.
func runningReasonOf(model statusmodel.Model) string {
	if model.WaitsOn != nil && !model.WaitsOn.NeedsPerson && model.WaitsOn.What != "" {
		return model.WaitsOn.What
	}
	if model.Lifecycle.Wave != nil {
		return fmt.Sprintf("wave %d of %d", model.Lifecycle.Wave.Active, model.Lifecycle.Wave.Total)
	}
	if model.Lifecycle.Phase != "" {
		return "in phase " + model.Lifecycle.Phase
	}
	return ""
}

// lastWordOf is a stopped run's reason: its own last word from the feed —
// the line the reconciler writes on every ending it reaches — rendered
// verbatim, its detail the run's sentence, never a shape the overview
// parses. A checkout that did not host the run holds no feed (the logs are
// exhaust, never pushed), and there the line names no reason rather than
// echoing the state word it already said: the run's DURABLE reason (the
// checkpoint's own) is a field the model does not carry, and a number a
// record does not state is stated not at all.
func lastWordOf(model statusmodel.Model) string {
	if model.Liveness.LastEvent != nil {
		return model.Liveness.LastEvent.Detail
	}
	return ""
}

// cloudEndReasonOf is a stopped CLOUD run's reason: its own last word from
// the factory's feed, and — where the factory served none — the liveness
// answer's own sentence, which for a cloud run is the record's terminal
// word said in full ("the Workflow's own record says completed"), never the
// local probe's process talk.
func cloudEndReasonOf(model statusmodel.Model) string {
	if model.Liveness.LastEvent != nil {
		return model.Liveness.LastEvent.Detail
	}
	return model.Liveness.Reason
}

// overviewRank orders the listing's bands: held first, failed next, running
// after, the terminal rest last.
func overviewRank(state string) int {
	switch state {
	case overviewStateHeld:
		return 0
	case overviewStateFailed:
		return 1
	case overviewStateRunning:
		return 2
	}
	return 3
}

// overviewStateWord is the state word a person reads — the one place the
// closed vocabulary meets prose.
func overviewStateWord(state string) string {
	switch state {
	case overviewStateHeld:
		return "held for a person"
	}
	return state
}

// renderOverview draws the prose listing the JSON answers with: one line per
// run, the state word, the reason, and — for a stop a person clears — the
// one command that clears it. A stop with no command (the merge, which is a
// person's by design and has no ticfac verb) is named by its reason, which
// already says what is wanted. The glance is the whole point, so the rows
// that need nobody — history (markOverviewHistory) — are one summary line
// naming the flag that lists them; with all, they are listed after the rest,
// each saying why it is history.
func renderOverview(stdout io.Writer, doc overviewModel, cloudNote string, all bool) {
	if len(doc.Runs) == 0 {
		fmt.Fprintln(stdout, "No runs.")
	}
	hidden := 0
	for _, run := range doc.Runs {
		if run.History && !all {
			hidden++
			continue
		}
		line := fmt.Sprintf("%s: %s", run.RunID, overviewStateWord(run.State))
		if run.Reason != "" {
			line += fmt.Sprintf(" — %s", run.Reason)
		}
		if run.History {
			line += fmt.Sprintf(" — history: %s", run.HistoryReason)
		} else if run.ClearWith != nil {
			line += fmt.Sprintf(" — clear with: %s", *run.ClearWith)
		}
		fmt.Fprintln(stdout, line)
	}
	if hidden > 0 {
		noun := "runs"
		if hidden == 1 {
			noun = "run"
		}
		fmt.Fprintf(stdout, "%d older %s not shown — epics closed, superseded by a later run, or finished more than %d days ago: `ticfac --all` lists them\n",
			hidden, noun, int(overviewHistoryAge.Hours()/24))
	}
	if cloudNote != "" {
		fmt.Fprintf(stdout, "cloud runs are not listed: %s\n", cloudNote)
	}
}
