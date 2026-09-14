package herdr

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// ExecutorName is this executor's name on the seam. An executor NAME crosses
// it; a concrete backend never does. It is what JobHandle.Executor carries and
// what a profile's `executor` field names for a herdr dispatch.
const ExecutorName = "herdr"

// PollInterval is the cadence at which a live job on this executor should be
// addressed: seconds, because herdr is a local substrate — nothing wipes an
// unaddressed job, and an Inspect is one socket round trip (tick u9l, epic
// av8). The five-minute DefaultPollInterval belongs to a cloud substrate that
// takes an unaddressed job AWAY, where the poll IS the keepalive; here the
// wait between polls is latency to notice a settle. herdr also has a push
// stream, which makes a slow poll worse than useless: it would sit over a
// signal that already arrived. The reconciler takes this through
// KnownExecutor.PollInterval, so the interval belongs to the executor rather
// than to one global constant.
const PollInterval = 5 * time.Second

// herdrHandle is this executor's private addressing, carried inside
// JobHandle.Handle — the ONE open object in the contract. Every piece of
// herdr addressing a caller can ever see is in here: workspace, pane, agent
// name, and the worktree herdr created, beside the identity facts the
// protocol records already spell (branch, base, write ref, result path).
//
// A handle carrying nothing but State is re-addressable: the attempt record
// in that directory holds the rest. That is the shape the reconciler's
// adoption and teardown paths construct, so decoding must survive it.
type herdrHandle struct {
	State       string `json:"state"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	WriteRef    string `json:"write_ref,omitempty"`
	BaseSHA     string `json:"base_sha,omitempty"`
	Repo        string `json:"repo,omitempty"`
	RepoKey     string `json:"repo_key,omitempty"`
	ResultPath  string `json:"result_path,omitempty"`
	ResultRel   string `json:"result_rel,omitempty"`
	TickID      string `json:"tick_id,omitempty"`
}

// local decodes the executor-private half of a handle, refusing one that
// names a different executor. A handle from another executor is not this
// package's to address, and quietly decoding it would be exactly the
// "one tick's job collected under another tick's name" failure the reconciler
// guards against on its side.
func local(h *subprocess.JobHandle) (*herdrHandle, error) {
	if h == nil {
		return nil, fmt.Errorf("no handle")
	}
	if h.Executor != "" && h.Executor != ExecutorName {
		return nil, fmt.Errorf("handle names executor %q; this is %s", h.Executor, ExecutorName)
	}
	raw, err := json.Marshal(h.Handle)
	if err != nil {
		return nil, err
	}
	var local herdrHandle
	if err := json.Unmarshal(raw, &local); err != nil {
		return nil, fmt.Errorf("handle payload: %w", err)
	}
	if local.State == "" {
		return nil, fmt.Errorf("handle payload carries no state directory: it cannot be re-addressed")
	}
	return &local, nil
}

// resolved fills the addressing a minimal handle does not carry from the
// attempt record in its state directory. The record is the authority: the
// handle is a frozen copy of it, and the copy is the thing a fresh
// controller could have lost.
func (l *herdrHandle) resolved() (*herdrHandle, *attemptRecord, error) {
	st := newStore(l.State)
	record, err := st.readAttempt()
	if err != nil {
		return l, nil, fmt.Errorf("no attempt record at %s: %w", l.State, err)
	}
	full := *l
	if full.WorkspaceID == "" {
		full.WorkspaceID = record.WorkspaceID
	}
	if full.PaneID == "" {
		full.PaneID = record.PaneID
	}
	if full.AgentName == "" {
		full.AgentName = record.AgentName
	}
	if full.Worktree == "" {
		full.Worktree = record.Worktree
	}
	if full.Branch == "" {
		full.Branch = record.Branch
	}
	if full.Repo == "" {
		full.Repo = record.Repo
	}
	if full.ResultPath == "" {
		full.ResultPath = record.ResultPath
		full.ResultRel = record.ResultRel
	}
	return &full, record, nil
}

// asMap is the handle's durable form.
func (l *herdrHandle) asMap() map[string]any {
	raw, err := json.Marshal(l)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// handleFor builds the handle for one attempt: the protocol's closed fields
// plus this executor's private addressing in the one open object.
func handleFor(record *attemptRecord) *subprocess.JobHandle {
	local := &herdrHandle{
		State:       record.State,
		WorkspaceID: record.WorkspaceID,
		PaneID:      record.PaneID,
		AgentName:   record.AgentName,
		Worktree:    record.Worktree,
		Branch:      record.Branch,
		WriteRef:    record.WriteRef,
		BaseSHA:     record.BaseSHA,
		Repo:        record.Repo,
		RepoKey:     record.RepoKey,
		ResultPath:  record.ResultPath,
		ResultRel:   record.ResultRel,
		TickID:      record.TickID,
	}
	return &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         record.JobID,
		Attempt:       record.Attempt,
		Executor:      ExecutorName,
		Handle:        local.asMap(),
		IssuedAt:      record.IssuedAt,
	}
}

// agentName is the herdr agent name for one attempt: readable in a sidebar,
// unique per attempt because attempt numbers are unique run-wide, and within
// herdr's [a-z][a-z0-9_-]{0,31} rule. The attempt suffix is what keeps a
// second attempt of the same tick from colliding with a first one whose
// agent is still live, so the 32-character budget is spent on the TICK ID
// and NEVER on the discriminator: a name cut to size after the suffix was
// appended drops exactly the part that makes it unique, and two attempts of
// one long tick id then share one agent name.
func agentName(tickID string, attempt int) string {
	sanitize := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
				b.WriteRune(r)
			default:
				b.WriteRune('-')
			}
		}
		return strings.Trim(b.String(), "-")
	}
	suffix := "-a" + fmt.Sprint(attempt)
	id := sanitize(tickID)
	if max := 32 - len("tick-") - len(suffix); max >= 0 && len(id) > max {
		// sanitize emits ASCII only, so the cut lands between characters,
		// never inside one.
		id = id[:max]
	}
	return "tick-" + id + suffix
}
