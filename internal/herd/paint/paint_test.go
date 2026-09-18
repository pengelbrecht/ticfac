package paint

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// fakeHerd records the calls paint makes and answers the session it was
// configured with. It implements the package's herd interface.
type fakeHerd struct {
	snapshot *client.SessionSnapshot
	snapErr  error

	panes      []client.PaneReportMetadataParams
	workspaces []client.WorkspaceReportMetadataParams
	reportErr  error
}

func (f *fakeHerd) SessionSnapshot(context.Context) (*client.SessionSnapshot, error) {
	return f.snapshot, f.snapErr
}

func (f *fakeHerd) PaneReportMetadata(_ context.Context, params client.PaneReportMetadataParams) error {
	if f.reportErr != nil {
		return f.reportErr
	}
	f.panes = append(f.panes, params)
	return nil
}

func (f *fakeHerd) WorkspaceReportMetadata(_ context.Context, params client.WorkspaceReportMetadataParams) error {
	if f.reportErr != nil {
		return f.reportErr
	}
	f.workspaces = append(f.workspaces, params)
	return nil
}

// liveSession is a session with one workspace, one pane with a live agent.
func liveSession() *client.SessionSnapshot {
	agent := "claude"
	name := "worker-a"
	return &client.SessionSnapshot{
		Workspaces: []client.WorkspaceInfo{{WorkspaceID: "ws-1"}},
		Panes: []client.PaneInfo{{
			PaneID:      "pane-1",
			WorkspaceID: "ws-1",
			AgentStatus: client.StatusWorking,
		}},
		Agents: []client.AgentInfo{{
			PaneID:      "pane-1",
			WorkspaceID: "ws-1",
			AgentStatus: client.StatusWorking,
			Agent:       &agent,
			Name:        &name,
		}},
	}
}

func TestRunPaintsLiveAttempt(t *testing.T) {
	h := &fakeHerd{snapshot: liveSession()}
	res, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{
			Tick: "a11", Epic: "9pd", Role: "implement-tick",
			WorkspaceID: "ws-1", PaneID: "pane-1",
		}},
		Statuses: map[string]string{"a11": "in_progress"},
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if res.Painted != 1 || res.Skipped != 0 {
		t.Fatalf("painted=%d skipped=%d, want 1/0", res.Painted, res.Skipped)
	}
	if len(h.workspaces) != 1 || h.workspaces[0].WorkspaceID != "ws-1" {
		t.Fatalf("workspace report: %+v", h.workspaces)
	}
	if !reflect.DeepEqual(h.workspaces[0].Tokens, map[string]string{
		"TICK": "a11", "ROLE": "implement-tick", "EPIC": "9pd", "STATUS": "in_progress",
	}) {
		t.Fatalf("workspace tokens: %+v", h.workspaces[0].Tokens)
	}
	if len(h.panes) != 1 {
		t.Fatalf("pane reports: %+v", h.panes)
	}
	if got := h.panes[0].Title; got == nil || *got != "a11 · implement-tick · in_progress" {
		t.Fatalf("pane title: %v", got)
	}
	if want := map[string]string{"working": "a11 in_progress"}; !reflect.DeepEqual(h.panes[0].StateLabels, want) {
		t.Fatalf("state labels: %+v", h.panes[0].StateLabels)
	}
	if h.panes[0].Source != client.SourceHerdPaint {
		t.Fatalf("source: %q", h.panes[0].Source)
	}
	if h.panes[0].TTL != DefaultTTL {
		t.Fatalf("ttl: %v", h.panes[0].TTL)
	}
}

func TestRunSkipsMissingWorkspaceAndDeadSession(t *testing.T) {
	h := &fakeHerd{snapshot: liveSession()}
	res, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{
			{Tick: "n01", PaneID: "pane-1"},                            // no workspace recorded
			{Tick: "n02", WorkspaceID: "ws-gone", PaneID: "pane-9"},    // workspace left the session
			{Tick: "n03", WorkspaceID: "ws-1", PaneID: "pane-other"},   // pane not in session
			{Tick: "n04", WorkspaceID: "ws-1", PaneID: "pane-foreign"}, // pane in another workspace
		},
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if res.Painted != 2 || res.Skipped != 2 {
		t.Fatalf("painted=%d skipped=%d, want the two live workspaces painted and the rest skipped", res.Painted, res.Skipped)
	}
	if len(h.panes) != 0 {
		t.Fatalf("no pane should have been reported: %+v", h.panes)
	}
	if len(h.workspaces) != 2 {
		t.Fatalf("the two live workspaces keep their tokens: %+v", h.workspaces)
	}
	byTick := map[string]Badge{}
	for _, b := range res.Badges {
		byTick[b.Tick] = b
	}
	if byTick["n02"].Skipped == "" {
		t.Fatalf("the dead workspace was not skipped: %+v", byTick["n02"])
	}
	// A pane the session does not know, or knows in somebody else's
	// workspace, is dropped from the badge: tokens only, never a title on
	// another window's pane.
	if byTick["n03"].PaneID != "" || byTick["n04"].PaneID != "" {
		t.Fatalf("foreign panes kept: %+v %+v", byTick["n03"], byTick["n04"])
	}
}

// A pane in another window's workspace is not painted even when its own
// workspace is live: a pane id is only evidence while the session agrees it
// sits where the attempt recorded it.
func TestRunPaintsWorkspaceTokensWhenPaneMoved(t *testing.T) {
	h := &fakeHerd{snapshot: &client.SessionSnapshot{
		Workspaces: []client.WorkspaceInfo{{WorkspaceID: "ws-1"}, {WorkspaceID: "ws-2"}},
		Panes:      []client.PaneInfo{{PaneID: "pane-1", WorkspaceID: "ws-2"}},
	}}
	res, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a12", WorkspaceID: "ws-1", PaneID: "pane-1"}},
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if res.Painted != 1 {
		t.Fatalf("painted=%d, want the workspace tokens alone", res.Painted)
	}
	if len(h.workspaces) != 1 || len(h.panes) != 0 {
		t.Fatalf("workspace=%d pane=%d, want 1/0", len(h.workspaces), len(h.panes))
	}
}

func TestRunDryRunReportsWithoutCalling(t *testing.T) {
	h := &fakeHerd{snapshot: liveSession()}
	res, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a13", WorkspaceID: "ws-1", PaneID: "pane-1"}},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if res.Painted != 1 || len(h.panes) != 0 || len(h.workspaces) != 0 {
		t.Fatalf("dry run reached herdr: %+v", res)
	}
}

func TestRunUnknownStatusAndTitle(t *testing.T) {
	if got := Title("t1", "", "open"); got != "t1 · open" {
		t.Fatalf("title: %q", got)
	}
	if got := Title("t1", "review-epic", ""); got != "t1 · review-epic" {
		t.Fatalf("title: %q", got)
	}
	h := &fakeHerd{snapshot: liveSession()}
	res, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a14", WorkspaceID: "ws-1"}},
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if res.Badges[0].TickStatus != UnknownStatus {
		t.Fatalf("tick status: %q", res.Badges[0].TickStatus)
	}
	if res.Badges[0].Tokens[TokenStatus] != UnknownStatus {
		t.Fatalf("status token: %q", res.Badges[0].Tokens[TokenStatus])
	}
}

func TestRunSeqOrdersReports(t *testing.T) {
	h := &fakeHerd{snapshot: liveSession()}
	if _, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a15", WorkspaceID: "ws-1"}},
		Seq:      7,
	}); err != nil {
		t.Fatalf("paint: %v", err)
	}
	if h.workspaces[0].Seq == nil || *h.workspaces[0].Seq != 7 {
		t.Fatalf("seq: %v", h.workspaces[0].Seq)
	}
}

func TestRunReportsErrors(t *testing.T) {
	h := &fakeHerd{snapshot: nil, snapErr: errors.New("no herdr")}
	if _, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a16", WorkspaceID: "ws-1"}},
	}); err == nil {
		t.Fatal("a session read failure must fail the run")
	}

	h = &fakeHerd{snapshot: liveSession(), reportErr: errors.New("herdr refused")}
	if _, err := Run(context.Background(), h, Options{
		Attempts: []Attempt{{Tick: "a17", WorkspaceID: "ws-1"}},
	}); err == nil {
		t.Fatal("a report refusal must fail the run")
	}
}
