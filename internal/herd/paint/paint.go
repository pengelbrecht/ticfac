// Package paint badges a run's herdr workspaces with the tick each worker is
// on — the operator surface `tk herd paint` provided before ticks' pwp
// deleted the herd surface (tick glb owns it here now).
//
// Ported from ticks' internal/herd/paint (deleted in ticks at 0b5fabc0), with
// one adaptation the extraction forces: tk's paint read the worker manifests
// `tk herd spawn` wrote under .tick/logs/herd, and that state no longer exists
// anywhere — ticfac's dispatch records live in the executor's own state root
// outside the repository. So the worker facts arrive as a plain [Attempt] list
// (the CLI reads them from the herdr executor's attempt records, and the tick
// statuses from the run checkpoint), and this package stays pure: it joins the
// attempts to a live herdr session snapshot and reports display-only metadata.
// Everything else is the shape ticks shipped: display-only, namespaced by the
// paint source, self-expiring through the TTL, and idempotent under repaint.
package paint

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// DefaultTTL is how long a painted badge survives without being repainted.
//
// It is deliberately short. The badge is a live status, so a stale one is
// worse than none: 90 seconds is long enough that the ordinary event stream
// (an agent changing status, a pane being focused) refreshes it well before it
// lapses, and short enough that an operator watching a dead run sees the
// badges drop within a minute rather than believing them.
const DefaultTTL = 90 * time.Second

// Separator joins the fields of a painted title. It is the herdr chrome's own
// separator, so a painted title reads like the rest of the UI.
const Separator = " · "

// UnknownStatus is the run status reported for a tick that could not be read
// — a checkpoint that could not be parsed, or an attempt whose tick is not in
// it. It is never silently blank: a badge that says "unknown" is honest, a
// badge with an empty status looks like a rendering bug.
const UnknownStatus = "unknown"

// Token names painted on both the pane and the workspace. herdr constrains
// them to `^[A-Za-z0-9_-]{1,32}$`, so they are upper-case and punctuation-free.
const (
	TokenTick   = "TICK"
	TokenRole   = "ROLE"
	TokenEpic   = "EPIC"
	TokenStatus = "STATUS"
)

// Attempt is one worker this run owns, as the executor recorded it: the
// addressing herdr handed out at spawn, plus the tick/epic/role the dispatch
// was for. It is the ticfac replacement for tk's spawn manifest.
type Attempt struct {
	// Tick is the tick id this worker implements. Required.
	Tick string
	// Epic is the parent epic id, or "" when the tick has no parent.
	Epic string
	// Role is the dispatch's role (implement-tick, review-epic, …).
	Role string
	// Label is the tick's short human label — its gloss, or its title cut
	// to the gloss width — or "" when the tracker could not be read. It
	// joins the pane title only; the tick token stays the bare id, because
	// the token is what matching reads.
	Label string
	// WorkspaceID is the herdr workspace the attempt lives in.
	WorkspaceID string
	// PaneID is the herdr pane the attempt's agent occupies.
	PaneID string
}

// Options is one paint run.
type Options struct {
	// Attempts are the workers this run owns. Nothing outside this set is
	// ever painted.
	Attempts []Attempt
	// Statuses maps tick id to the run's recorded tick state; a tick with
	// no entry paints as [UnknownStatus]. The run's checkpoint is the one
	// source the CLI reads this from — paint never opens the tracker.
	Statuses map[string]string
	// Source namespaces the metadata reports. Empty means
	// [client.SourceHerdPaint]; override it only in tests.
	Source string
	// TTL is how long a badge survives. Zero means [DefaultTTL].
	TTL time.Duration
	// Seq, when non-zero, orders this run's reports against its own
	// earlier ones. Zero omits `seq`, which makes every report apply — the
	// right default for a painter invoked from an event hook, where two
	// events can be in flight at once and neither is "later".
	Seq uint64
	// DryRun builds the badges and reports what WOULD be painted without
	// calling herdr. The plan is built by the same code either way.
	DryRun bool
}

// Badge is what one worker's workspace and pane are painted with, plus the
// evidence for why. A skipped badge still carries everything it would have
// painted, so `--json` explains the decision rather than just announcing it.
type Badge struct {
	// Tick, Epic and Role come from the attempt.
	Tick string `json:"tick"`
	Epic string `json:"epic,omitempty"`
	Role string `json:"role,omitempty"`
	// TickStatus is the run-recorded state of the tick, or
	// [UnknownStatus].
	TickStatus string `json:"tick_status"`
	// AgentStatus is the live herdr agent status of the worker's pane,
	// empty when the pane is not in the session.
	AgentStatus string `json:"agent_status,omitempty"`

	// WorkspaceID and PaneID are the attempt's recorded targets.
	WorkspaceID string `json:"workspace_id,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`

	// Label is the tick's short human label the title carries, when known.
	Label string `json:"label,omitempty"`
	// Title is the pane title: `<tick-id> (<label>) · <role> · <status>`.
	Title string `json:"title"`
	// StateLabels is the label herdr shows while the pane is in the
	// worker's current agent status. Empty when the pane is not live.
	StateLabels map[string]string `json:"state_labels,omitempty"`
	// Tokens are the badge pairs, painted identically on pane and
	// workspace.
	Tokens map[string]string `json:"tokens"`

	// PaintedWorkspace and PaintedPane record what was actually reported.
	// Both are false under DryRun.
	PaintedWorkspace bool `json:"painted_workspace"`
	PaintedPane      bool `json:"painted_pane"`
	// Skipped is why nothing was painted, empty when something was.
	Skipped string `json:"skipped,omitempty"`
}

// Painted reports whether this badge reached herdr at all.
func (b Badge) Painted() bool { return b.PaintedWorkspace || b.PaintedPane }

// Result is the outcome of one paint run.
type Result struct {
	// Badges is one entry per attempt, sorted by tick id.
	Badges []Badge `json:"badges"`
	// Source and TTLMs are what the reports were sent with — the two
	// things an operator needs to reason about what they are seeing.
	Source string `json:"source"`
	TTLMs  uint64 `json:"ttl_ms"`
	// Painted and Skipped count the badges.
	Painted int `json:"painted"`
	Skipped int `json:"skipped"`
	// DryRun echoes whether anything was actually reported.
	DryRun bool `json:"dry_run"`
}

// herd is the slice of the herdr client this package needs. It is an
// interface so the paint decision can be tested without a socket; the live
// implementation is *client.Client.
type herd interface {
	SessionSnapshot(ctx context.Context) (*client.SessionSnapshot, error)
	PaneReportMetadata(ctx context.Context, params client.PaneReportMetadataParams) error
	WorkspaceReportMetadata(ctx context.Context, params client.WorkspaceReportMetadataParams) error
}

// Run paints every workspace this run owns and returns what it did.
//
// An error means the run could not be performed at all (herdr unreachable). A
// worker whose workspace has gone away is a skipped badge, not an error —
// attempt records routinely outlive their workspaces.
func Run(ctx context.Context, h herd, opts Options) (Result, error) {
	source := opts.Source
	if source == "" {
		source = client.SourceHerdPaint
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if opts.Statuses == nil {
		opts.Statuses = map[string]string{}
	}

	res := Result{
		Source: source,
		TTLMs:  uint64(ttl / time.Millisecond),
		DryRun: opts.DryRun,
		Badges: []Badge{},
	}

	snap, err := h.SessionSnapshot(ctx)
	if err != nil {
		return res, fmt.Errorf("herd/paint: reading the herdr session: %w", err)
	}
	live := indexSession(snap)

	var seq *uint64
	if opts.Seq != 0 {
		seq = client.Ptr(opts.Seq)
	}

	for _, m := range opts.Attempts {
		status := opts.Statuses[m.Tick]
		b := badgeFor(m, status, live)
		if b.Skipped == "" && !opts.DryRun {
			if err := paintOne(ctx, h, &b, source, seq, ttl); err != nil {
				return res, err
			}
		}
		if b.Skipped != "" {
			res.Skipped++
		} else if opts.DryRun || b.Painted() {
			res.Painted++
		}
		res.Badges = append(res.Badges, b)
	}
	sort.Slice(res.Badges, func(i, j int) bool { return res.Badges[i].Tick < res.Badges[j].Tick })
	return res, nil
}

// paintOne reports one badge to herdr: the workspace tokens first, then the
// pane, so a failure halfway leaves the workspace decorated rather than a pane
// title with no matching workspace tokens.
func paintOne(ctx context.Context, h herd, b *Badge, source string, seq *uint64, ttl time.Duration) error {
	if b.WorkspaceID != "" {
		err := h.WorkspaceReportMetadata(ctx, client.WorkspaceReportMetadataParams{
			WorkspaceID: b.WorkspaceID,
			Source:      source,
			Tokens:      b.Tokens,
			Seq:         seq,
			TTL:         ttl,
		})
		if err != nil {
			return fmt.Errorf("herd/paint: painting workspace %s for tick %s: %w", b.WorkspaceID, b.Tick, err)
		}
		b.PaintedWorkspace = true
	}
	if b.PaneID != "" {
		err := h.PaneReportMetadata(ctx, client.PaneReportMetadataParams{
			PaneID:      b.PaneID,
			Source:      source,
			Title:       client.Ptr(b.Title),
			StateLabels: b.StateLabels,
			Tokens:      b.Tokens,
			Seq:         seq,
			TTL:         ttl,
		})
		if err != nil {
			return fmt.Errorf("herd/paint: painting pane %s for tick %s: %w", b.PaneID, b.Tick, err)
		}
		b.PaintedPane = true
	}
	return nil
}

// sessionIndex is the live session, reduced to the two questions paint asks:
// does this workspace still exist, and which workspace is this pane in.
type sessionIndex struct {
	workspaces  map[string]bool
	paneOwner   map[string]string
	paneStatus  map[string]string
	agentStatus map[string]string
}

// indexSession folds a snapshot into the lookups paint needs. Panes and agents
// are both consulted: herdr reports an agent-bearing pane in `agents`, and a
// snapshot need not repeat it in `panes`.
func indexSession(snap *client.SessionSnapshot) sessionIndex {
	idx := sessionIndex{
		workspaces:  map[string]bool{},
		paneOwner:   map[string]string{},
		paneStatus:  map[string]string{},
		agentStatus: map[string]string{},
	}
	if snap == nil {
		return idx
	}
	for _, w := range snap.Workspaces {
		idx.workspaces[w.WorkspaceID] = true
	}
	for _, p := range snap.Panes {
		idx.paneOwner[p.PaneID] = p.WorkspaceID
		idx.paneStatus[p.PaneID] = string(p.AgentStatus)
	}
	for _, a := range snap.Agents {
		idx.paneOwner[a.PaneID] = a.WorkspaceID
		idx.agentStatus[a.PaneID] = string(a.AgentStatus)
	}
	return idx
}

// statusOf reports a pane's live agent status, preferring the agent record —
// the pane entry's status is the same field, but the agent list is where herdr
// keeps it current for an agent-bearing pane.
func (idx sessionIndex) statusOf(paneID string) string {
	if s, ok := idx.agentStatus[paneID]; ok && s != "" {
		return s
	}
	return idx.paneStatus[paneID]
}

// badgeFor builds one worker's badge and decides whether it may be painted.
func badgeFor(m Attempt, tickStatus string, live sessionIndex) Badge {
	if tickStatus == "" {
		tickStatus = UnknownStatus
	}
	b := Badge{
		Tick:        m.Tick,
		Epic:        m.Epic,
		Role:        m.Role,
		TickStatus:  tickStatus,
		WorkspaceID: m.WorkspaceID,
		PaneID:      m.PaneID,
		Label:       m.Label,
		Title:       Title(tickRef(m), m.Role, tickStatus),
		Tokens:      tokensFor(m, tickStatus),
	}

	if b.WorkspaceID == "" {
		b.Skipped = "the attempt record carries no workspace"
		return b
	}
	// Ownership guard: the attempt says this run created the workspace, and
	// the session says it is still there. A workspace id that has been
	// recycled onto somebody else's window would fail the pane check below.
	if !live.workspaces[b.WorkspaceID] {
		b.Skipped = "workspace " + b.WorkspaceID + " is not in the herdr session (already cleaned up?)"
		b.PaneID = ""
		return b
	}

	// The pane is painted only when the session agrees it sits in the
	// workspace this run owns. Anything else is another window's pane.
	if b.PaneID != "" {
		owner, known := live.paneOwner[b.PaneID]
		switch {
		case !known:
			b.PaneID = ""
		case owner != b.WorkspaceID:
			b.PaneID = ""
		default:
			b.AgentStatus = live.statusOf(b.PaneID)
			if b.AgentStatus != "" {
				b.StateLabels = map[string]string{
					b.AgentStatus: m.Tick + " " + tickStatus,
				}
			}
		}
	}
	return b
}

// tickRef names the attempt's tick to a person: `<tick-id> (<label>)`, or the
// bare id when no label is known.
func tickRef(m Attempt) string {
	if strings.TrimSpace(m.Label) == "" {
		return m.Tick
	}
	return m.Tick + " (" + m.Label + ")"
}

// Title renders the painted pane title: `<tick-id> · <role> · <status>`,
// where the tick is named `<tick-id> (<label>)` when a label is known.
// Empty fields are dropped rather than rendered as a gap, so an attempt with
// no role still produces a readable title.
func Title(tickID, role, status string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{tickID, role, status} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, Separator)
}

// tokensFor is the badge pair set, painted identically on the pane and the
// workspace. Empty values are omitted: herdr renders a token with an empty
// value as a blank badge.
func tokensFor(m Attempt, tickStatus string) map[string]string {
	tokens := map[string]string{
		TokenTick:   m.Tick,
		TokenStatus: tickStatus,
	}
	if m.Role != "" {
		tokens[TokenRole] = m.Role
	}
	if m.Epic != "" {
		tokens[TokenEpic] = m.Epic
	}
	return tokens
}
