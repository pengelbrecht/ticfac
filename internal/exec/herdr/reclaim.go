package herdr

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// IDENTITY, NOT BOOKKEEPING (tick 5hz).
//
// A herdr restart — an upgrade, a protocol bump — is exactly when the
// workspace ids this executor recorded drift from the ids herdr hands out
// afterwards. Seven canonify workspaces were orphaned that way: the tick was
// closed, the manifest that named the workspace was gone, and the recorded id
// named nothing, so no teardown could ever find them again. The rule this
// file implements is the one the incident asks for: before anything is
// removed, ASK HERDR WHAT EXISTS, and attribute the workspace to the
// attempt by the evidence herdr itself carries — a workspace whose worktree
// path is this attempt's worktree is this attempt's workspace; so is one
// whose branch is this attempt's branch.
//
// The same question answers the `workspace_not_found` hazard: an id herdr
// does not know is TWO different facts — "this workspace is gone" and "this
// id is unknown" — and only herdr's own answer to "what exists" separates
// them. Silence refuses: the fail-closed rule x6j applies to substrate
// answers generally, applied to the identity question.

const (
	// RefusedWorkspaceUnattributed is the refusal a teardown answers when
	// herdr still holds a workspace under the RECORDED id but nothing ties
	// that workspace to this attempt — its worktree and its branch match no
	// workspace herdr will name. The id may be stale onto somebody else's
	// workspace, so removing it could take another attempt's work with it;
	// the teardown refuses rather than guess.
	RefusedWorkspaceUnattributed = "workspace_unattributed"

	// RefusedReclaimUnauthorised is the refusal a reclamation answers when
	// nothing authorised it. Removal is destructive; the report is the
	// report, and only an operator or a declared policy moves it to a
	// removal.
	RefusedReclaimUnauthorised = "reclaim_unauthorised"
)

// workspaceSurvey is herdr's own answer to "what exists right now": every
// workspace the session holds, and — when herdr will answer it — the
// repository's worktrees, each naming the workspace it is open in and the
// branch it holds.
type workspaceSurvey struct {
	snapshot *client.SessionSnapshot
	// listing is nil when herdr answered the snapshot but not the list:
	// the snapshot is the authority on what EXISTS, the list only carries
	// corroborating evidence (branch → workspace), and its absence makes
	// attribution fail closed rather than silently weaker.
	listing *client.WorktreeListing
}

// survey asks herdr what exists. It is the ONE question whose answer can
// distinguish a gone workspace from a stale id, so its own failure is not a
// caller's to absorb: the error is returned and every caller of this
// refuses on it.
func (e *Executor) survey(ctx context.Context) (*workspaceSurvey, error) {
	snapshot, err := e.client.SessionSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("herdr could not be asked what exists: %w", err)
	}
	listing, listErr := e.client.WorktreeList(ctx, client.WorktreeListParams{Cwd: client.Ptr(e.repo)})
	if listErr != nil {
		listing = nil
	}
	return &workspaceSurvey{snapshot: snapshot, listing: listing}, nil
}

// attribution is the survey's verdict on ONE attempt's workspace.
type attribution struct {
	// id is the id herdr holds this attempt's workspace under, "" when the
	// survey found none of this attempt's evidence anywhere.
	id string
	// evidence names HOW the workspace was attributed — "its worktree
	// <path>", "its branch <branch>" — so the record a person reads says
	// what was matched, not just that something was.
	evidence string
	// gone says herdr answered and NOTHING it has belongs to this attempt:
	// the corroborated "already gone", distinct from an id nobody can ask
	// about.
	gone bool
	// recordedKnown says herdr still holds a workspace under the RECORDED
	// id — the dangerous half: the id names SOMETHING, and whether that
	// something is this attempt's is exactly what the evidence answers.
	recordedKnown bool
	// collides says the recorded id names a DIFFERENT workspace than the
	// evidence did: a restart re-used the id for somebody else's work.
	collides bool
}

// attribute resolves ONE attempt's workspace from the survey, by evidence
// first and the recorded id LAST — the id is bookkeeping, and bookkeeping is
// the thing a restart strands.
func (sv *workspaceSurvey) attribute(record *attemptRecord, recordedID string) attribution {
	att := attribution{}
	if id, evidence, ok := sv.idByPath(record); ok {
		att.id, att.evidence = id, evidence
	} else if id, evidence, ok := sv.idByBranch(record); ok {
		att.id, att.evidence = id, evidence
	}
	for _, ws := range sv.snapshot.Workspaces {
		if ws.WorkspaceID == recordedID {
			att.recordedKnown = true
		}
	}
	att.gone = att.id == "" && !att.recordedKnown
	att.collides = att.recordedKnown && att.id != recordedID
	return att
}

// idByPath is the strongest evidence: the worktree path herdr checked this
// attempt's workspace out at is written in this executor's own attempt
// record, and a workspace sitting at that path is this attempt's whatever
// id herdr calls it today.
func (sv *workspaceSurvey) idByPath(record *attemptRecord) (string, string, bool) {
	for _, ws := range sv.snapshot.Workspaces {
		if ws.Worktree != nil && samePath(ws.Worktree.CheckoutPath, record.Worktree) {
			return ws.WorkspaceID, fmt.Sprintf("its worktree %s", record.Worktree), true
		}
	}
	if sv.listing != nil {
		for _, wt := range sv.listing.Worktrees {
			if wt.OpenWorkspaceID != nil && *wt.OpenWorkspaceID != "" && samePath(wt.Path, record.Worktree) {
				return *wt.OpenWorkspaceID, fmt.Sprintf("its worktree %s", record.Worktree), true
			}
		}
	}
	return "", "", false
}

// idByBranch is the second evidence: the ONE ref this job may write is
// durable on origin, and a worktree holding that branch is this attempt's.
func (sv *workspaceSurvey) idByBranch(record *attemptRecord) (string, string, bool) {
	if sv.listing == nil {
		return "", "", false
	}
	for _, wt := range sv.listing.Worktrees {
		if wt.Branch != nil && *wt.Branch == record.Branch &&
			wt.OpenWorkspaceID != nil && *wt.OpenWorkspaceID != "" {
			return *wt.OpenWorkspaceID, fmt.Sprintf("its branch %s", record.Branch), true
		}
	}
	return "", "", false
}

// attributeForTeardown is the identity question asked immediately before the
// removal — the same place the liveness question is asked — because a herdr
// restart can move ids between any earlier answer and the destructive step.
//
// Its two refusals are the halves of `workspace_not_found` this tick exists
// to split apart: herdr not answering refuses (the id may be stale and
// nobody can ask), and a recorded id that names a workspace nothing ties to
// this attempt refuses (the id may be stale onto somebody else's workspace).
func (e *Executor) attributeForTeardown(record *attemptRecord, recordedID string) (*attribution, error) {
	sv, err := e.survey(context.Background())
	if err != nil {
		return nil, refuse(subprocess.RefusedUnknown,
			"%v: the recorded workspace id %s may be stale, and a removal that cannot ask what exists "+
				"refuses — the same fail-closed rule the liveness question is held to",
			err, recordedID)
	}
	att := sv.attribute(record, recordedID)
	if att.id == "" && att.recordedKnown {
		return nil, refuse(RefusedWorkspaceUnattributed,
			"herdr still has a workspace under the recorded id %s, but nothing ties it to this attempt — "+
				"neither its worktree %s nor its branch %s matches any workspace herdr will name. The id may be "+
				"stale onto somebody else's workspace: removing it could take another attempt's work with it, "+
				"so this teardown refuses rather than guess",
			recordedID, record.Worktree, record.Branch)
	}
	return &att, nil
}

// samePath compares two worktree paths as paths, so a trailing separator or
// a doubled slash can never make one workspace count as two.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// ------------------------------------------------------------ the report ---

// Orphan is one herdr workspace the report names as reclaimable: a
// workspace whose tick — by the evidence herdr itself carries — the caller
// says is closed, so no run will ever come back for it. Listing orphans
// removes nothing; that is what the report is for.
type Orphan struct {
	// WorkspaceID is the id herdr holds the workspace under RIGHT NOW — not
	// a remembered one.
	WorkspaceID string `json:"workspace_id"`
	// Worktree and Branch are the evidence that tied the workspace to a
	// tick of this repository.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Label is the workspace's own label, carried because it is the
	// weakest of the three evidences and the record should show it was
	// all that was available when it was.
	Label string `json:"label,omitempty"`
	// TickID is the tick the evidence named.
	TickID string `json:"tick_id"`
	// Evidence says how the tick was recognised — "its branch …", "its
	// worktree path …", "its label …" — so a person reading the report
	// can check the attribution before authorising anything.
	Evidence string `json:"evidence"`
}

// Reclaimable reports the herdr workspaces this repository holds whose tick
// the caller says is CLOSED — the reclamation-at-startup report. Removal is
// destructive, so the listing removes NOTHING: what moves a report to a
// removal is an authorisation, on exactly the set reported here.
//
// The executor does not know which ticks are closed or which belong to this
// epic; that is the tracker's authority and the caller holds it. What this
// package owns is the evidence herdr itself carries — the branch a worktree
// holds, the path it sits at, the label it was created under — and the
// honest tick each one names. A workspace whose tick cannot be derived is
// left out of the report rather than guessed into it: a workspace nothing
// can attribute is a thing a person looks at, not a thing a report claims.
func (e *Executor) Reclaimable(ctx context.Context, closed func(tickID string) bool) ([]Orphan, error) {
	if closed == nil {
		return nil, fmt.Errorf("reclaim: a report needs the caller's authority on tick state — " +
			"a listing that cannot ask which ticks are closed would either list everything or nothing, " +
			"and both are lies")
	}
	sv, err := e.survey(ctx)
	if err != nil {
		return nil, fmt.Errorf("reclaim: %w", err)
	}
	var out []Orphan
	for _, ws := range sv.snapshot.Workspaces {
		path := ""
		if ws.Worktree != nil {
			path = ws.Worktree.CheckoutPath
		}
		branch := sv.branchAt(path)
		tick, evidence := tickOfFacts(branch, path, ws.Label)
		if tick == "" {
			continue
		}
		if !closed(tick) {
			continue
		}
		out = append(out, Orphan{
			WorkspaceID: ws.WorkspaceID, Worktree: path, Branch: branch,
			Label: ws.Label, TickID: tick, Evidence: evidence,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkspaceID < out[j].WorkspaceID })
	return out, nil
}

// branchAt is the branch the survey's worktree listing says the worktree at
// path holds, "" when herdr did not answer the listing or the path is not
// among the repository's worktrees.
func (sv *workspaceSurvey) branchAt(path string) string {
	if sv.listing == nil || path == "" {
		return ""
	}
	for _, wt := range sv.listing.Worktrees {
		if samePath(wt.Path, path) && wt.Branch != nil {
			return *wt.Branch
		}
	}
	return ""
}

// tickOfFacts names the tick the herdr-side facts point at, strongest
// evidence first: the branch (the one ref a job may write is durable on
// origin), then the worktree path, then the label. The tick segment of a
// write ref is "tick-<id>" (SPEC §4.3's golden shape,
// ticfac/run-<run>/tick-<tick>/attempt-<n>); the path and label name the
// tick the same way when they follow it.
func tickOfFacts(branch, path, label string) (tick, evidence string) {
	if id := tickFromSegments(branch); id != "" {
		return id, "its branch " + branch
	}
	if id := tickFromSegments(path); id != "" {
		return id, "its worktree path " + path
	}
	// The label: ticks' own herd machinery labelled workspaces "tick-<id>",
	// and this executor's Start passes the bare tick id. A bare label is
	// the weakest evidence — any word could sit in it — so it is passed to
	// the caller's own authority on tick ids rather than trusted here.
	if label == "" {
		return "", ""
	}
	if id := tickFromSegments(label); id != "" {
		return id, "its label " + label
	}
	return label, "its label " + label
}

// tickFromSegments finds a "tick-<id>" segment in a slash-separated value,
// the segment a write ref's tick takes.
func tickFromSegments(value string) string {
	for _, seg := range strings.Split(value, "/") {
		if id, ok := strings.CutPrefix(seg, "tick-"); ok && id != "" {
			return id
		}
	}
	return ""
}

// ------------------------------------------------------- the authorisation ---

// ReclaimOutcome is what one authorised removal did.
type ReclaimOutcome struct {
	WorkspaceID string `json:"workspace_id"`
	TickID      string `json:"tick_id"`
	// Removed says the workspace was actually torn down.
	Removed bool `json:"removed"`
	// Note says what happened instead when Removed is false — a refusal
	// and why, or that herdr no longer holds the workspace at all.
	Note string `json:"note,omitempty"`
}

// Reclaim removes EXACTLY the orphans whose removal was authorised — the
// half the report is the other half of. authorisedBy names the operator or
// the declared policy that decided, and a reclamation without one is
// refused before anything is touched.
//
// Every removal re-derives its facts, next to the destructive step, the
// same way disposal does: herdr is asked what exists, the workspace must
// still sit where the report said it did, and an agent still working or
// blocked on the pane refuses — the liveness rule teardown is held to. The
// workspace a restart has since moved under another id is refused as
// unattributed rather than guessed at.
func (e *Executor) Reclaim(ctx context.Context, orphans []Orphan, authorisedBy string) ([]ReclaimOutcome, error) {
	if authorisedBy == "" {
		return nil, refuse(RefusedReclaimUnauthorised,
			"a reclamation is a removal, and a removal needs an authorisation: report first, "+
				"then remove only what the operator or a declared policy named")
	}
	var outcomes []ReclaimOutcome
	for _, orphan := range orphans {
		sv, err := e.survey(ctx)
		if err != nil {
			return outcomes, fmt.Errorf("reclaim %s: %w", orphan.WorkspaceID, err)
		}
		ws := sv.workspaceByID(orphan.WorkspaceID)
		if ws == nil {
			outcomes = append(outcomes, ReclaimOutcome{WorkspaceID: orphan.WorkspaceID, TickID: orphan.TickID,
				Note: "herdr no longer holds this workspace: nothing to remove"})
			continue
		}
		// The evidence re-checked, next to the removal: the workspace must
		// still be the one the report described.
		if ws.Worktree != nil && orphan.Worktree != "" && !samePath(ws.Worktree.CheckoutPath, orphan.Worktree) {
			outcomes = append(outcomes, ReclaimOutcome{WorkspaceID: orphan.WorkspaceID, TickID: orphan.TickID,
				Note: fmt.Sprintf("the workspace's worktree moved (now %s): the report is stale, re-report "+
					"before authorising again", ws.Worktree.CheckoutPath)})
			continue
		}
		// Liveness, next to the removal, in the teardown vocabulary.
		switch ws.AgentStatus {
		case client.StatusWorking, client.StatusBlocked:
			outcomes = append(outcomes, ReclaimOutcome{WorkspaceID: orphan.WorkspaceID, TickID: orphan.TickID,
				Note: fmt.Sprintf("the workspace's agent is %s: it may be mid-turn about to commit, or it is "+
					"waiting on a person's answer — the pane is the handoff state, not a thing to tear down",
					ws.AgentStatus)})
			continue
		}
		if _, err := e.client.WorktreeRemove(ctx, client.WorktreeRemoveParams{
			WorkspaceID: orphan.WorkspaceID, Force: false,
		}); err != nil && !client.IsCode(err, client.CodeWorkspaceNotFound) {
			return outcomes, fmt.Errorf("reclaim %s: herdr worktree.remove: %w", orphan.WorkspaceID, err)
		}
		outcomes = append(outcomes, ReclaimOutcome{WorkspaceID: orphan.WorkspaceID, TickID: orphan.TickID,
			Removed: true, Note: "removed by " + authorisedBy})
	}
	return outcomes, nil
}

// workspaceByID is the workspace herdr holds under an id, nil when it holds
// none — the report's own re-read, on the survey it just took.
func (sv *workspaceSurvey) workspaceByID(id string) *client.WorkspaceInfo {
	for i, ws := range sv.snapshot.Workspaces {
		if ws.WorkspaceID == id {
			return &sv.snapshot.Workspaces[i]
		}
	}
	return nil
}
