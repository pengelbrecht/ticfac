package cli

// The model half of `ticfac status` (ticfac tick 6dh): the gathering that
// feeds [statusmodel.Build] — the versioned answer `status --json` emits,
// covering a run's lifecycle, its waves and ticks, its live workers, what it
// waits on, its health, its CI and its cost.
//
// The gathering is deliberately best-effort PER SOURCE: a source that cannot
// be read costs the model that source's fields and a name in `degraded`,
// never the whole answer — the questions an unattended factory is glanced at
// with are ordered precisely so that "does anything need me" survives a
// tracker that cannot be read or a forge that cannot be asked. What may
// never degrade is honesty: a field with nothing behind it is null, not a
// guess.
//
// Everything read here already existed: the tracker's graph, the run's
// durable records on origin (or the run directory a checkout holds), the
// event feed, the worktree census, the runner's session logs, the forge's
// check runs. No new source of truth is created and none is written.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// epicGraph is the epic's own orchestration graph, read through `tk graph --all`
// — the same layering `tk graph` computes, with the tracker's CLOSED tasks
// included, because the epic's whole table is the dashboard's rows. The read
// is from the INTEGRATION BRANCH on origin, where the epic's tracker writes
// are durable (status_tracker.go) — an operator watching a cloud run from a
// main checkout would otherwise see every closed tick open — and falls back
// to the checkout's own tree, the shape every caller has always read, when
// the branch cannot be served. A tracker that cannot be read in either
// place costs the model its waves and nothing else: the map comes back nil,
// the model says so, and every other field answers. Swappable so a test
// fakes the tracker without a binary.
var epicGraph = func(ctx context.Context, repo, epicID string) *tk.Graph {
	if epicID == "" {
		return nil
	}
	if graph := graphAtIntegrationBranch(ctx, repo, epicID); graph != nil {
		return graph
	}
	client, err := tk.NewContext(ctx, tk.Options{Dir: repo})
	if err != nil {
		return nil
	}
	graph, err := client.GraphAll(ctx, epicID)
	if err != nil {
		// The manifest-pinned read, for a tracker whose binary predates the
		// flag: the open tasks alone are still the layering tk computes.
		if graph, err := client.Graph(ctx, epicID); err == nil {
			return &graph
		}
		return nil
	}
	return &graph
}

var (
	statusRecordsMu    sync.Mutex
	statusRecordsLocks = map[string]*sync.Mutex{}
)

// lockStatusRecords takes the lock for one checkout's run and returns its
// release.
func lockStatusRecords(key string) func() {
	statusRecordsMu.Lock()
	lock, ok := statusRecordsLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		statusRecordsLocks[key] = lock
	}
	statusRecordsMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// statusRecords reads the run's durable records — and, beside them, every
// EARLIER run's records for the same epic from the same fetched branch
// (hn6, tick gmo): the epic's state is what every run left on the
// integration branch, and the newest run's own records are only the newest
// layer of it. The run is read under the id the surface names (a cloud run's
// factory run_<hex> — the container execs `run-epic --run-id $TICKS_RUN_ID`,
// so its records land there), falling back to the epic spelling an older
// container wrote (tick tem's era) when the named id carries nothing. Through
// the run-state store (origin — durable means pushed, and the store is the
// authority every other surface reads) and, where no remote can be fetched,
// from the run directories the checkout holds. The fallback is a fallback
// for checkouts without a remote, not a second opinion: what origin says,
// goes.
func statusRecords(repo, runID, epicID string) (statusmodel.Records, []statusmodel.Records, error) {
	// One store fetch at a time per run in a checkout: the store fetches into
	// a ref private to this PROCESS and run, so two concurrent reads of the
	// same run here (the overview gathers cloud runs concurrently, and every
	// cloud run of one epic reads the same epic-<id> records) would race on
	// that ref's lock and one would come back degraded.
	unlock := lockStatusRecords(repo + "\x00" + runID)
	defer unlock()
	if epicID != "" {
		store, err := runstate.Open(runstate.Options{Repo: repo, Branch: "epic/" + epicID, RunID: runID})
		if err == nil {
			if _, fetchErr := store.Fetch(); fetchErr == nil {
				// The run the surface names first — the id every other
				// surface addresses the run by. A run dir with no checkpoint
				// is no record the run wrote; the epic spelling behind it is
				// the older container's layout, and the honest second try.
				for _, candidate := range runRecordCandidates(runID, epicID) {
					_, ok, err := store.Read(runstate.CheckpointPath(candidate))
					if err != nil {
						return statusmodel.Records{}, nil, err
					}
					if !ok {
						continue
					}
					records, err := statusmodel.RecordsFromStoreRun(store, candidate)
					if err != nil {
						return statusmodel.Records{}, nil, err
					}
					prior, err := statusPriorRecords(store, candidate, epicID)
					if err != nil {
						return statusmodel.Records{}, nil, err
					}
					return records, prior, nil
				}
				// The branch carries nothing for either spelling: the run has
				// written no durable record — the honest empty answer, and
				// every prior run's records beside it.
				prior, err := statusPriorRecords(store, "", epicID)
				if err != nil {
					return statusmodel.Records{}, nil, err
				}
				return statusmodel.Records{}, prior, nil
			}
		}
	}
	for _, candidate := range runRecordCandidates(runID, epicID) {
		records, err := statusmodel.RecordsFromDir(filepath.Join(repo, runstate.Root, "runs", candidate))
		if err != nil {
			return statusmodel.Records{}, nil, err
		}
		if records.Checkpoint != nil {
			prior, err := statusPriorRecordsFromDir(repo, candidate, epicID)
			if err != nil {
				return statusmodel.Records{}, nil, err
			}
			return records, prior, nil
		}
	}
	prior, err := statusPriorRecordsFromDir(repo, runID, epicID)
	if err != nil {
		return statusmodel.Records{}, nil, err
	}
	return statusmodel.Records{}, prior, nil
}

// runRecordCandidates is the run ids one run's records may sit under, in the
// order they are tried: the id the surface names (today's writer — the
// container execs `run-epic --run-id $TICKS_RUN_ID`), then the epic spelling
// an older container's default run id wrote.
func runRecordCandidates(runID, epicID string) []string {
	candidates := []string{runID}
	if epicID != "" {
		if epic := "epic-" + epicID; epic != runID {
			candidates = append(candidates, epic)
		}
	}
	return candidates
}

// statusPriorRecords is every OTHER run's records for one epic, as the
// fetched branch carries them, oldest first — the runs whose work the
// dashboard's rows and progress count. A sibling run is one whose own
// checkpoint names this epic and is not the run being read; a run with no
// readable checkpoint is no run of this epic, not an error.
func statusPriorRecords(store *runstate.Store, currentRunID, epicID string) ([]statusmodel.Records, error) {
	ids := []string{}
	for _, path := range store.List(runstate.Root + "/runs") {
		// The path under `.ticfac/runs/` names the run; only checkpoint
		// records identify a run as this epic's.
		rest, ok := strings.CutPrefix(path, runstate.Root+"/runs/")
		if !ok || !strings.HasSuffix(path, "/checkpoint.json") {
			continue
		}
		runID := strings.TrimSuffix(rest, "/checkpoint.json")
		if strings.ContainsAny(runID, "/") || runID == currentRunID || runID == "" {
			continue
		}
		ids = append(ids, runID)
	}
	readCheckpoint := func(id string) (*runstate.Checkpoint, bool) {
		raw, ok, err := store.Read(runstate.CheckpointPath(id))
		if err != nil || !ok {
			return nil, false
		}
		var checkpoint runstate.Checkpoint
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&checkpoint); err != nil {
			// A sibling record that does not read back clean is not this
			// read's business: the run's OWN records refuse a drifted
			// record, and a sibling's drift is named by its own reader.
			return nil, false
		}
		return &checkpoint, true
	}
	readAll := func(id string) (statusmodel.Records, error) {
		records, err := statusmodel.RecordsFromStoreRun(store, id)
		if err != nil {
			return statusmodel.Records{}, fmt.Errorf("read run %s's records: %w", id, err)
		}
		return records, nil
	}
	return priorRunsForEpic(ids, currentRunID, epicID, readCheckpoint, readAll)
}

// statusPriorRecordsFromDir is the same every-run read where no remote can
// be fetched: the sibling run directories this checkout holds.
func statusPriorRecordsFromDir(repo, currentRunID, epicID string) ([]statusmodel.Records, error) {
	if epicID == "" {
		return nil, nil
	}
	runsDir := filepath.Join(repo, runstate.Root, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, nil // no runs on disk at all: the honest empty answer
	}
	ids := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != currentRunID {
			ids = append(ids, entry.Name())
		}
	}
	readCheckpoint := func(id string) (*runstate.Checkpoint, bool) {
		raw, err := os.ReadFile(filepath.Join(runsDir, id, "checkpoint.json"))
		if err != nil {
			return nil, false
		}
		var checkpoint runstate.Checkpoint
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&checkpoint); err != nil {
			return nil, false
		}
		return &checkpoint, true
	}
	readAll := func(id string) (statusmodel.Records, error) {
		records, err := statusmodel.RecordsFromDir(filepath.Join(runsDir, id))
		if err != nil {
			return statusmodel.Records{}, fmt.Errorf("read run %s's records: %w", id, err)
		}
		return records, nil
	}
	return priorRunsForEpic(ids, currentRunID, epicID, readCheckpoint, readAll)
}

// priorRunsForEpic is the sibling selection both readers share: of the run
// ids given, keep those whose checkpoint reads back clean and names the
// epic, excluding the run being read, ordered oldest checkpoint first. A
// run whose records cannot then be read whole is an error — a sibling
// named by its own checkpoint is a run whose records exist, and half of a
// run's history is a history that lies.
func priorRunsForEpic(
	ids []string, currentRunID, epicID string,
	readCheckpoint func(id string) (*runstate.Checkpoint, bool),
	readAll func(id string) (statusmodel.Records, error),
) ([]statusmodel.Records, error) {
	type priorRun struct {
		id        string
		updatedAt string
	}
	var priors []priorRun
	for _, id := range ids {
		if id == currentRunID || id == "" {
			continue
		}
		checkpoint, ok := readCheckpoint(id)
		if !ok || checkpoint.EpicID != epicID {
			continue
		}
		priors = append(priors, priorRun{id: id, updatedAt: checkpoint.UpdatedAt})
	}
	sort.Slice(priors, func(i, j int) bool { return priors[i].updatedAt < priors[j].updatedAt })
	out := []statusmodel.Records{}
	for _, prior := range priors {
		records, err := readAll(prior.id)
		if err != nil {
			return nil, err
		}
		out = append(out, records)
	}
	return out, nil
}

// statusCI asks the forge about the epic PR: the state the close-out's gate
// reads, and the latest run of every check behind it. No token, no remote or
// no PR is not an error — it is the honest "no forge answer", and the model
// carries null. An error a configured forge raises IS reported, because a
// forge that cannot be asked when it should be is a fact a person reads.
//
// A seam like epicGraph's, for the same reason: the forge is the
// environment's, and a test that needs the model to see a PR answers it
// with a controlled value.
var statusCI = func(ctx context.Context, repo, epicID string) (*statusmodel.CIInput, error) {
	if epicID == "" {
		return nil, nil
	}
	// The same credential ladder doctor and the run read (tick vo4). A ladder
	// with no rung that answers is "no token" — the honest no-forge answer.
	token, _, err := resolveForgeToken()
	if err != nil || token == "" {
		return nil, nil
	}
	out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").Output()
	if err != nil {
		return nil, nil // no remote: this checkout is not one the forge mirrors
	}
	ownerName, err := forge.ParseRepo(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("resolve the repository the forge mirrors: %w", err)
	}
	github := forge.GitHub{Token: token, Repo: ownerName, Refresh: forge.FactoryTokenSourceFromEnv()}
	pr, err := github.Find(ctx, "epic/"+epicID, "")
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, nil
	}
	checks, err := github.Checks(ctx, *pr)
	if err != nil {
		return nil, err
	}
	report, err := github.CI(ctx, *pr)
	if err != nil {
		return nil, err
	}
	input := &statusmodel.CIInput{
		State:  string(report.State),
		Checks: []statusmodel.CheckState{},
		PR: &statusmodel.PR{
			Number:  pr.Number,
			URL:     pr.URL,
			HeadRef: pr.HeadRef,
			HeadSHA: pr.HeadSHA,
			BaseRef: pr.BaseRef,
		},
	}
	for _, c := range checks {
		input.Checks = append(input.Checks, statusmodel.CheckState{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			StartedAt:  c.StartedAt,
		})
	}
	return input, nil
}

// statusBuild is the model's assembly point, a seam in the shape of
// epicGraph and statusCI above it: both gathering functions hand their
// Sources to THIS and never to statusmodel.Build directly, so a test can
// observe what the wiring passes. The dashboard's wave-1 readers (hn6,
// tick r5i) answer nil — the honest not-measured — which means an emitted
// model cannot tell a wired gathering from an unwired one: the Sources are
// the only place the wire is observable, and the command-level tests hold
// it there.
var statusBuild = statusmodel.Build

// statusWorkerCost is a LOCAL run's own gateway-joined spend (tick dm2): the
// operator's AI Gateway logs, read back through the same client the cloud
// trace reads them with, filtered to the rows stamped with this run id —
// the rows the metering join's cf-aig-metadata tag produces. Where the
// logs answer calls for the run, the sum of their measured cost is the
// host's ground-truth number the model's workers-ai line meters (the same
// claim the factory's own cost sync makes for a cloud run); where they
// answer none — the calls were never joined, or the host states no
// gateway — the answer is nil and the line says "not metered", the honest
// word for spend no measurement names.
//
// A host whose ~/.ticfacrc names no gateway or no token is the documented
// OPTIONAL state, nil and never an error: cost telemetry is optional, and
// a status answer must never degrade over it. An error from a gateway that
// SHOULD have answered (a revoked token, an unreachable API) IS reported —
// degraded, like every source this model cannot read.
var statusWorkerCost = func(ctx context.Context, runID string) (*statusmodel.WorkerCostInput, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	file, err := credentials.Load()
	if err != nil {
		return nil, err
	}
	config, err := gatewaytrace.ConfigFrom(file)
	if err != nil {
		return nil, nil // not configured: the optional state, not a failed read
	}
	// The same test-and-override knob the jev credential resolution honours
	// (its $TICFAC_JEV_API_BASE), so a test points the reader at a server of
	// its own without touching production's default.
	if base := strings.TrimSpace(os.Getenv(jev.OperatorBaseEnv)); base != "" {
		config.APIBase = base
	}
	client := gatewaytrace.New(config, nil)
	calls, err := client.Calls(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, nil
	}
	totals := gatewaytrace.Sum(calls)
	return &statusmodel.WorkerCostInput{USD: totals.Cost, Source: "gateway"}, nil
}

// modelGatherers is the per-frame source policy for a FOLLOWING surface:
// `status --json` answers once and pays every read on the way, but the live
// watch (89m) re-gathers every frame, and a frame every two seconds must
// not spawn a tracker subprocess or ask a forge every two seconds — the rule
// `status --follow` already set for its labels. The one-shot surface passes
// the direct reads; the watch passes the caches (watch.go).
type modelGatherers struct {
	graph      func(context.Context, string, string) *tk.Graph
	ci         func(context.Context, string, string) (*statusmodel.CIInput, error)
	workerCost func(context.Context, string) (*statusmodel.WorkerCostInput, error)
}

// epicIDOf derives the epic id a run id names: `epic-<id>` for a local run,
// the checkpoint's own epic_id when the records carry one, and the caller's
// word for a cloud run (its record carries the epic).
func epicIDOf(runID string, records statusmodel.Records) string {
	if records.Checkpoint != nil && records.Checkpoint.EpicID != "" {
		return records.Checkpoint.EpicID
	}
	if rest, ok := strings.CutPrefix(runID, "epic-"); ok {
		return rest
	}
	return ""
}

// priorFeedsLocal is every prior run's own event feed, keyed by the run id its
// records were read under — the facts about a hold that only the feed states
// (the records say a tick was rejected; only the line says it was held FOR
// A PERSON). Local runs are read from the logs this checkout keeps,
// `.ticfac/logs/<run-id>/events.jsonl`; a cloud run's from the factory's
// events route per run id. Best-effort per run: a prior feed that cannot be
// read is a missing hint, not a degraded source — the run's records carry
// the rest of its history, and an unreadable feed must never cost the model
// its answer.
func priorFeedsLocal(repo string, prior []statusmodel.Records) map[string][]runfeed.Event {
	feeds := map[string][]runfeed.Event{}
	for _, records := range prior {
		if records.Checkpoint == nil || records.Checkpoint.RunID == "" {
			continue
		}
		if events, err := runfeed.Read(runfeed.Path(repo, records.Checkpoint.RunID)); err == nil {
			feeds[records.Checkpoint.RunID] = events
		}
	}
	return feeds
}

// priorFeedsCloud is the same read for a cloud run's earlier runs, through
// the factory's events route per run id — the same route the run's own feed
// comes from, and the only place its prior runs' feeds exist.
func priorFeedsCloud(ctx context.Context, client *cloudClient, warn io.Writer, prior []statusmodel.Records) map[string][]runfeed.Event {
	feeds := map[string][]runfeed.Event{}
	for _, records := range prior {
		if records.Checkpoint == nil || records.Checkpoint.RunID == "" {
			continue
		}
		source := &cloudFeedSource{client: client, runID: records.Checkpoint.RunID, warn: warn}
		located, absent, err := feedStanding(ctx, source)
		if err != nil || absent {
			continue
		}
		events := make([]runfeed.Event, 0, len(located))
		for _, line := range located {
			events = append(events, line.Event)
		}
		feeds[records.Checkpoint.RunID] = events
	}
	return feeds
}

// localStatusModel gathers everything a LOCAL run's model reads and builds
// it. The liveness answer is the probe's own, carried as data; every age the
// model states is measured against the one `now` the gathering stamps.
//
// The probe is deliberately PER-CHECKOUT (tick aj9): status is a snapshot of
// what this checkout can read, and --repo names another. A surface that reads
// runs from MANY checkouts — the bare `ticfac` listing (2qz) — must instead
// resolve each run's working repo through internal/runregistry first and
// probe THERE; that package's doc comment is the convention.
func localStatusModel(ctx context.Context, repo, runID string, probe runlife.Status, gather modelGatherers) statusmodel.Model {
	return localStatusModelHosted(ctx, repo, runID, probe, gather, statusmodel.HostLocal)
}

// localStatusModelHosted is the gathering the local model and the cloud
// container's pushed model share (hn6 h7w): everything a run's own host
// machine can read — its feed, its records on origin, the tracker, the forge,
// the worktree census — with the HOST as the caller states it. The run-epic
// inside an orchestrator container IS local to that container (its pidfile
// probes, its feed reads), but the run it works is the factory's cloud run,
// so the model it pushes names the host the reader must see: "cloud", whose
// clearing commands are the factory's, never a local `run-epic` on the
// machine reading the page. The one host-dependent input is the host itself
// — every source the gathering reads is the machine's own either way.
func localStatusModelHosted(ctx context.Context, repo, runID string, probe runlife.Status, gather modelGatherers, host string) statusmodel.Model {
	now := time.Now()
	degraded := []string{}

	records, prior, err := statusRecords(repo, runID, epicHintOf(runID))
	if err != nil {
		records = statusmodel.Records{}
		degraded = append(degraded, "run-state")
	}
	epicID := epicIDOf(runID, records)

	graph := gather.graph(ctx, repo, epicID)
	if graph == nil {
		degraded = append(degraded, "tracker")
	}

	var feed []runfeed.Event
	if events, err := runfeed.Read(runfeed.Path(repo, runID)); err == nil {
		feed = events
	} else {
		degraded = append(degraded, "feed")
	}

	standing, standingErr := runprogress.Standing(repo, runID, now)

	ci, ciErr := gather.ci(ctx, repo, epicID)
	if ciErr != nil {
		degraded = append(degraded, "forge")
	}

	priorFeeds := priorFeedsLocal(repo, prior)

	// The local run's own gateway-joined spend (tick dm2): the logs read
	// under the run id the surface named, metering the workers-ai line where
	// the run's calls were joined to the gateway and answering nil — the
	// honest not-measured — where they were not. An error from a gateway
	// that should have answered degrades the model's cost, like every
	// source this model cannot read; a host that states no gateway is the
	// documented optional state and degrades nothing.
	var workerCost *statusmodel.WorkerCostInput
	if gather.workerCost != nil {
		if cost, costErr := gather.workerCost(ctx, runID); costErr == nil {
			workerCost = cost
		} else {
			degraded = append(degraded, "cost")
		}
	}

	home, _ := os.UserHomeDir()
	session := func(worktree string) *statusmodel.Turn {
		return statusmodel.SessionLog(home, worktree)
	}

	return statusBuild(statusmodel.Sources{
		Now:          now,
		RunID:        runID,
		Host:         host,
		EpicID:       epicID,
		Degraded:     degraded,
		Graph:        graph,
		Records:      &records,
		PriorRecords: prior,
		PriorFeeds:   priorFeeds,
		Feed:         feed,
		Standing:     standing,
		StandingRead: standingErr == nil,
		Liveness: statusmodel.LivenessInput{
			Alive:  probe.State == runlife.Alive,
			State:  string(probe.State),
			Reason: probe.Reason,
			Source: "run.pid",
		},
		Session: session,
		// The dashboard's per-worker and per-tick readers (hn6 wave 1): the
		// activity window from the runner's transcript, the attempt reports
		// from the run's records, and — since zl1 — the worker's executor-own
		// name from the attempt record in the dispatch's state directory on
		// this machine. All are nil-safe stubs where nothing answers.
		Activity:   statusmodel.TranscriptActivity(home),
		Report:     statusmodel.AttemptReports(repo),
		Handle:     statusmodel.WorkerHandles(runID),
		WorkerCost: workerCost,
		CI:         ci,
	})
}

// epicHintOf is the cheap first guess at a run's epic id, refined by the
// records once they are read.
func epicHintOf(runID string) string {
	if rest, ok := strings.CutPrefix(runID, "epic-"); ok {
		return rest
	}
	return ""
}

// cloudRecordBelongsToRepo is whether a factory run record is one THIS
// checkout's project can claim — the rule that decides whether this repo's
// records, tracker and PR may be read for it (tick nyi). A record that names
// a different project than the one this checkout mirrors is never this
// repo's to read. Anything less — a checkout that names no project (no
// origin to mirror), a record that carries none — attributes the run to
// nobody else, and the local evidence this checkout holds is the best answer
// there is, so the read stands: the defect is the misattribution, not the
// attempt.
func cloudRecordBelongsToRepo(repoProject, recordProject string) bool {
	repoProject = strings.TrimSpace(repoProject)
	recordProject = strings.TrimSpace(recordProject)
	if repoProject == "" || recordProject == "" {
		return true
	}
	return repoProject == recordProject
}

// cloudStatusModel gathers everything a CLOUD run's model reads and builds
// it. A cloud run's workers are not on this machine: the census is not
// taken, and the model's workers field states null — "cannot be counted
// here", which is a different claim from "none stand". Its records live on
// origin like any run's; its feed is the factory's own stream.
//
// The records, the tracker and the forge are read ONLY for a run this
// checkout's project can claim (tick nyi): a factory run of ANOTHER project
// — the same factory may host several — has its records on that project's
// origin, its ticks in that project's tracker and its PR against that
// project's base, none of which this checkout can read. Reading this repo's
// for it answers questions about the wrong epic: this repo's own untriaged
// findings would render a foreign run "held for a person". The model says
// what it could not read, in `degraded`, and the entry-level surfaces hold
// the one rule that matters there: no row for another project's run names a
// command this checkout could run for it.
func cloudStatusModel(ctx context.Context, client *cloudClient, repo, runID string, record cloudRunRecord, liveness cloudLiveness, warn io.Writer, gather modelGatherers, ours bool) statusmodel.Model {
	now := time.Now()
	degraded := []string{}

	epicID := strings.TrimSpace(record.Epic)

	// The records a cloud run reads are on the integration branch under the
	// id the run was written under. The container execs `ticfac run-epic
	// --run-id $TICKS_RUN_ID` (the factory's run_<hex>, hn0), so TODAY's
	// records land there — read under the id the surface names. tem's era
	// spelled it epic-<epic-id>; statusRecords tries both and answers what
	// exists. The model still NAMES the factory's run id — the id every
	// surface addresses the run by.
	recordsID := runID
	var records statusmodel.Records
	var prior []statusmodel.Records
	if ours {
		if read, priors, err := statusRecords(repo, recordsID, epicID); err == nil {
			records = read
			prior = priors
		} else {
			degraded = append(degraded, "run-state")
		}
	} else {
		// Another project's run: its records live on that project's origin,
		// which this checkout cannot read — named, never guessed at from this
		// repo's own records for an epic id it happens to share.
		degraded = append(degraded, "foreign-run")
	}
	if epicID == "" {
		epicID = epicIDOf(runID, records)
	}

	var graph *tk.Graph
	if ours {
		graph = gather.graph(ctx, repo, epicID)
	}
	if graph == nil {
		degraded = append(degraded, "tracker")
	}

	var feed []runfeed.Event
	source := &cloudFeedSource{client: client, runID: runID, warn: warn}
	if located, absent, err := feedStanding(ctx, source); err == nil && !absent {
		for _, line := range located {
			feed = append(feed, line.Event)
		}
	} else if err != nil {
		degraded = append(degraded, "feed")
	}

	var priorFeeds map[string][]runfeed.Event
	if ours {
		priorFeeds = priorFeedsCloud(ctx, client, warn, prior)
	}

	var ci *statusmodel.CIInput
	if ours {
		input, ciErr := gather.ci(ctx, repo, epicID)
		if ciErr != nil {
			degraded = append(degraded, "forge")
		}
		ci = input
	}

	// The factory's own ground-truth cost, ONLY when its record says the
	// number is the gateway's measurement: the runs row's cost_usd is NOT
	// NULL DEFAULT 0, so a bare number is not a measurement — a run before
	// its first cost sync, or one whose telemetry reads "unavailable: …",
	// is an unsynced record and the model's cost lines answer empty (tick
	// 1tm). A record that carries no number states none either.
	var workerCost *statusmodel.WorkerCostInput
	if record.CostSource != nil && strings.TrimSpace(*record.CostSource) == "gateway" && record.CostUSD != nil {
		workerCost = &statusmodel.WorkerCostInput{USD: *record.CostUSD, Source: "gateway"}
	}

	return statusBuild(statusmodel.Sources{
		Now:          now,
		RunID:        runID,
		Host:         statusmodel.HostCloud,
		EpicID:       epicID,
		Degraded:     degraded,
		Graph:        graph,
		Records:      &records,
		PriorRecords: prior,
		PriorFeeds:   priorFeeds,
		Feed:         feed,
		StandingRead: false, // a cloud run's worktrees are not on this machine
		Liveness: statusmodel.LivenessInput{
			Alive:  liveness.Alive,
			State:  liveness.State,
			Reason: liveness.Reason,
			Source: liveness.Source,
		},
		// A cloud run's runners and attempt reports are not on this machine
		// (its worktrees belong to the factory's containers): the readers pass
		// nil and the model leaves the fields null, the honest not-measured.
		Activity:   nil,
		Report:     nil,
		WorkerCost: workerCost,
		CI:         ci,
	})
}

// printStatusModel emits the model as the --json surface's answer: indented,
// one object, versioned. The exit code stays liveness's alone — a script
// branching on `status --json` branches on the run's life, never on the
// model's content.
func printStatusModel(stdout, stderr io.Writer, model statusmodel.Model) error {
	raw, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", raw)
	return nil
}
