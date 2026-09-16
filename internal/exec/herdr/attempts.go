package herdr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// AttemptFacts is the herdr addressing of one attempt, as the executor's own
// record states it — the read-only input the operator commands (the
// `ticfac herd paint` and `ticfac herd notify` surfaces, tick glb) join to a
// live herdr session.
//
// It is exported as FACTS and nothing else: the record this is read from is
// the executor's private state, and a consumer that wanted more of it would be
// a second executor, which the seam forbids. Only what a decoration needs is
// visible: identity, role, and the herdr addressing.
type AttemptFacts struct {
	// RunID is the run the attempt belongs to, from the directory layout
	// the reconciler gave the executor (<state-root>/<run>/<tick>/<attempt>).
	RunID string
	// TickID and Attempt name the dispatch.
	TickID  string
	Attempt int
	// Role is the dispatch's role, from the job spec.
	Role string
	// WorkspaceID and PaneID are the herdr addressing the spawn produced.
	// WorkspaceID is the CURRENT id — the record keeps any stale id
	// separately — so a herdr restart that moved ids is already settled
	// here. PaneID is the pane the agent occupies, the primary join key for
	// the live agent list.
	WorkspaceID string
	PaneID      string
	// AgentName is the agent's herdr name, the join fallback for a record
	// written before a pane id was captured.
	AgentName string
	// EpicID is the tick's parent epic when the run recorded one; paint
	// shows it as a workspace token. Derived from the run id, which the
	// reconciler names "epic-<epic-id>" — the same convention the events
	// feed and the dashboard read.
	EpicID string
}

// epicIDFromRun peels the epic id off a run id the reconciler named
// "epic-<epic-id>". A run id of any other shape has no epic to show, and ""
// is a value paint handles (the EPIC token is omitted), not an error.
func epicIDFromRun(runID string) string {
	const prefix = "epic-"
	if len(runID) > len(prefix) && runID[:len(prefix)] == prefix {
		return runID[len(prefix):]
	}
	return ""
}

// Attempts reads one run's herdr attempts from the executor's state root:
// every attempt record the run wrote, reduced to the LATEST attempt of each
// tick.
//
// Latest-per-tick is the only honest choice for a decoration: the reconciler
// dispatches a NEW attempt when it wants the tick worked again, and an earlier
// attempt's workspace is either torn down (dead, the session join will say) or
// superseded (painting it would badge a worker the run has moved on from).
//
// A missing run directory is an empty list, not an error — the command this
// serves runs from event hooks, and a run with no herdr attempts is the
// ordinary state of a repo between runs. An attempt record that cannot be
// read is skipped with the reason reported, because one damaged directory
// must not cost the operator the whole wave's badges.
func Attempts(stateRoot, runID string) ([]AttemptFacts, error) {
	runDir := filepath.Join(stateRoot, filepath.FromSlash(runID))
	entries, err := os.ReadDir(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("herdr: reading %s: %w", runDir, err)
	}

	latest := map[string]AttemptFacts{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tickID := entry.Name()
		attempts, err := os.ReadDir(filepath.Join(runDir, tickID))
		if err != nil {
			return nil, fmt.Errorf("herdr: reading %s: %w", filepath.Join(runDir, tickID), err)
		}
		for _, a := range attempts {
			if !a.IsDir() {
				continue
			}
			n, err := strconv.Atoi(a.Name())
			if err != nil {
				continue // not an attempt directory: the settlements dir, a stray
			}
			facts, err := readAttemptFacts(filepath.Join(runDir, tickID, a.Name()), runID, tickID, n)
			if err != nil {
				continue // a damaged record is skipped, never fatal to the wave
			}
			if prior, seen := latest[tickID]; !seen || n > prior.Attempt {
				latest[tickID] = facts
			}
		}
	}

	out := make([]AttemptFacts, 0, len(latest))
	for _, facts := range latest {
		out = append(out, facts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TickID < out[j].TickID })
	return out, nil
}

// readAttemptFacts reads one attempt directory's attempt.json through the
// executor's own reader, so there is exactly one reader of the record's
// schema in this repository.
func readAttemptFacts(dir, runID, tickID string, attempt int) (AttemptFacts, error) {
	raw, err := os.ReadFile(filepath.Join(dir, fileAttempt))
	if err != nil {
		return AttemptFacts{}, err
	}
	var record attemptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return AttemptFacts{}, err
	}
	// The record is the executor's own, but this is a read-only path: a
	// record whose schema has moved past this build is skipped rather than
	// guessed at.
	if record.SchemaVersion != stateSchemaVersion {
		return AttemptFacts{}, fmt.Errorf("attempt record schema_version %d, want %d", record.SchemaVersion, stateSchemaVersion)
	}
	if record.TickID != tickID || record.Attempt != attempt {
		return AttemptFacts{}, fmt.Errorf("attempt record at %s names %s attempt %d, not %s attempt %d", dir, record.TickID, record.Attempt, tickID, attempt)
	}
	role := ""
	if record.Spec != nil {
		role = record.Spec.Role
	}
	return AttemptFacts{
		RunID:       runID,
		TickID:      record.TickID,
		Attempt:     record.Attempt,
		Role:        role,
		WorkspaceID: record.WorkspaceID,
		PaneID:      record.PaneID,
		AgentName:   record.AgentName,
		EpicID:      epicIDFromRun(runID),
	}, nil
}
