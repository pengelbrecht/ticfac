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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// epicGraph is the epic's own orchestration graph, read through `tk graph`
// the same way `tickLabels` reads `tk list` — the same layering the run
// dispatches by. A tracker that cannot be read costs the model its waves and
// nothing else: the map comes back nil, the model says so, and every other
// field answers. Swappable so a test fakes the tracker without a binary.
var epicGraph = func(ctx context.Context, repo, epicID string) *tk.Graph {
	if epicID == "" {
		return nil
	}
	client, err := tk.NewContext(ctx, tk.Options{Dir: repo})
	if err != nil {
		return nil
	}
	graph, err := client.Graph(ctx, epicID)
	if err != nil {
		return nil
	}
	return &graph
}

// statusRecords reads the run's durable records: through the run-state store
// (origin — durable means pushed, and the store is the authority every other
// surface reads) and, where no remote can be fetched, from the run directory
// the checkout holds. The fallback is a fallback for checkouts without a
// remote, not a second opinion: what origin says, goes.
func statusRecords(repo, runID, epicID string) (statusmodel.Records, error) {
	if epicID != "" {
		store, err := runstate.Open(runstate.Options{Repo: repo, Branch: "epic/" + epicID, RunID: runID})
		if err == nil {
			if _, err := store.Fetch(); err == nil {
				return statusmodel.RecordsFromStore(store)
			}
		}
	}
	return statusmodel.RecordsFromDir(filepath.Join(repo, runstate.Root, "runs", runID))
}

// statusCI asks the forge about the epic PR: the state the close-out's gate
// reads, and the latest run of every check behind it. No token, no remote or
// no PR is not an error — it is the honest "no forge answer", and the model
// carries null. An error a configured forge raises IS reported, because a
// forge that cannot be asked when it should be is a fact a person reads.
func statusCI(ctx context.Context, repo, epicID string) (*statusmodel.CIInput, error) {
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
	github := forge.GitHub{Token: token, Repo: ownerName}
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

// localStatusModel gathers everything a LOCAL run's model reads and builds
// it. The liveness answer is the probe's own, carried as data; every age the
// model states is measured against the one `now` the gathering stamps.
func localStatusModel(ctx context.Context, repo, runID string, probe runlife.Status) statusmodel.Model {
	now := time.Now()
	degraded := []string{}

	records, err := statusRecords(repo, runID, epicHintOf(runID))
	if err != nil {
		records = statusmodel.Records{}
		degraded = append(degraded, "run-state")
	}
	epicID := epicIDOf(runID, records)

	graph := epicGraph(ctx, repo, epicID)
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

	ci, ciErr := statusCI(ctx, repo, epicID)
	if ciErr != nil {
		degraded = append(degraded, "forge")
	}

	home, _ := os.UserHomeDir()
	session := func(worktree string) *statusmodel.Turn {
		return statusmodel.SessionLog(home, worktree)
	}

	return statusmodel.Build(statusmodel.Sources{
		Now:          now,
		RunID:        runID,
		Host:         statusmodel.HostLocal,
		EpicID:       epicID,
		Degraded:     degraded,
		Graph:        graph,
		Records:      &records,
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
		CI:      ci,
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

// cloudStatusModel gathers everything a CLOUD run's model reads and builds
// it. A cloud run's workers are not on this machine: the census is not
// taken, and the model's workers field states null — "cannot be counted
// here", which is a different claim from "none stand". Its records live on
// origin like any run's; its feed is the factory's own stream.
func cloudStatusModel(ctx context.Context, client *cloudClient, repo, runID string, record cloudRunRecord, liveness cloudLiveness, warn io.Writer) statusmodel.Model {
	now := time.Now()
	degraded := []string{}

	epicID := strings.TrimSpace(record.Epic)

	records, err := statusRecords(repo, runID, epicID)
	if err != nil {
		records = statusmodel.Records{}
		degraded = append(degraded, "run-state")
	}
	if epicID == "" {
		epicID = epicIDOf(runID, records)
	}

	graph := epicGraph(ctx, repo, epicID)
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

	ci, ciErr := statusCI(ctx, repo, epicID)
	if ciErr != nil {
		degraded = append(degraded, "forge")
	}

	return statusmodel.Build(statusmodel.Sources{
		Now:          now,
		RunID:        runID,
		Host:         statusmodel.HostCloud,
		EpicID:       epicID,
		Degraded:     degraded,
		Graph:        graph,
		Records:      &records,
		Feed:         feed,
		StandingRead: false, // a cloud run's worktrees are not on this machine
		Liveness: statusmodel.LivenessInput{
			Alive:  liveness.Alive,
			State:  liveness.State,
			Reason: liveness.Reason,
			Source: liveness.Source,
		},
		CI: ci,
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
