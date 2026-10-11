package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// fakePane is the herdr half: Start answers startErr, and every call is
// counted so a test can say which executor was asked.
type fakePane struct {
	startErr   error
	abandonErr error
	starts     int
	abandoned  []string
	asked      []string
}

func (f *fakePane) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	f.starts++
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &subprocess.JobHandle{JobID: spec.JobID, Executor: herdr.ExecutorName}, nil
}
func (f *fakePane) Inspect(*subprocess.JobHandle, string) (*subprocess.JobStatus, error) {
	f.asked = append(f.asked, "inspect")
	return &subprocess.JobStatus{}, nil
}
func (f *fakePane) CollectDetail(*subprocess.JobHandle) (*subprocess.Collection, error) {
	f.asked = append(f.asked, "collect")
	return &subprocess.Collection{}, nil
}
func (f *fakePane) Cancel(*subprocess.JobHandle) (*subprocess.CancelAck, error) {
	f.asked = append(f.asked, "cancel")
	return &subprocess.CancelAck{}, nil
}
func (f *fakePane) Dispose(*subprocess.JobHandle, subprocess.DisposeOptions) error {
	f.asked = append(f.asked, "dispose")
	return nil
}
func (f *fakePane) AbandonUnstartedLaunch(spec *subprocess.JobSpec, reason string) error {
	f.abandoned = append(f.abandoned, spec.JobID)
	return f.abandonErr
}

// fakeLocal is the headless half, recording the dispatch it was built for.
type fakeLocal struct {
	built   []reconcile.Dispatch
	starts  int
	asked   []string
	handles []*subprocess.JobHandle
}

func (f *fakeLocal) factory(d reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error) {
	f.built = append(f.built, d)
	return f, reconcile.Substrate{}, nil
}
func (f *fakeLocal) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	f.starts++
	// The local executor's own attempt record, where it keeps it.
	state := filepath.Join(f.built[len(f.built)-1].StateDir, "repokey", "attemptkey")
	if err := os.MkdirAll(state, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(state, "attempt.json"), []byte("{}"), 0o644); err != nil {
		return nil, err
	}
	return &subprocess.JobHandle{JobID: spec.JobID, Executor: subprocess.ExecutorName,
		Handle: map[string]any{"state": state}}, nil
}
func (f *fakeLocal) Inspect(h *subprocess.JobHandle, _ string) (*subprocess.JobStatus, error) {
	f.handles = append(f.handles, h)
	f.asked = append(f.asked, "inspect")
	return &subprocess.JobStatus{}, nil
}
func (f *fakeLocal) CollectDetail(*subprocess.JobHandle) (*subprocess.Collection, error) {
	f.asked = append(f.asked, "collect")
	return &subprocess.Collection{}, nil
}
func (f *fakeLocal) Cancel(*subprocess.JobHandle) (*subprocess.CancelAck, error) {
	f.asked = append(f.asked, "cancel")
	return &subprocess.CancelAck{}, nil
}
func (f *fakeLocal) Dispose(*subprocess.JobHandle, subprocess.DisposeOptions) error {
	f.asked = append(f.asked, "dispose")
	return nil
}

func paneDispatch(t *testing.T, runner string) reconcile.Dispatch {
	return reconcile.Dispatch{
		RunID: "r1", TickID: "t1", Role: "review", StateDir: t.TempDir(),
		Profile: &profile.Profile{Role: "review", Executor: herdr.ExecutorName, Runner: runner, Model: "opus"},
	}
}

func stuckAtTrust() error {
	return &herdr.PaneStartError{Kind: "claude", Agent: "tick-t1-a1", PaneID: "w1:p1",
		Worktree: "/home/op/.herdr/worktrees/repo/ticfac-run-epic-e-tick-t1-attempt-1",
		Dialog:   herdr.DialogFolderTrust, DialogName: "its folder-trust dialog", Redispatched: true}
}

// A pane that cannot start — after herdr's own one re-dispatch — runs the
// job on the headless runner of the same profile, under a state root of its
// own, with a feed note saying why; and every later operation goes to the
// executor the handle names (tick o35).
func TestAPaneThatCannotStartFallsBackToTheHeadlessRunner(t *testing.T) {
	t.Parallel()
	d := paneDispatch(t, "claude")
	pane := &fakePane{startErr: stuckAtTrust()}
	local := &fakeLocal{}
	w := withHeadlessFallback(pane, d, local.factory)

	spec := &subprocess.JobSpec{JobID: "r1/tick-t1/attempt-1"}
	// Before any fallback a herdr handle is herdr's.
	if _, err := w.Inspect(&subprocess.JobHandle{Executor: herdr.ExecutorName}, ""); err != nil {
		t.Fatal(err)
	}
	if len(pane.asked) != 1 || len(local.asked) != 0 {
		t.Fatalf("a herdr handle before any fallback went to local %v / herdr %v", local.asked, pane.asked)
	}
	handle, err := w.Start(spec)
	if err != nil {
		t.Fatalf("a pane that cannot start halted the dispatch: %v", err)
	}
	if handle.Executor != subprocess.ExecutorName || local.starts != 1 {
		t.Fatalf("the job did not start headless: handle %+v, local starts %d", handle, local.starts)
	}
	if len(pane.abandoned) != 1 {
		t.Errorf("the never-prompted pane was not abandoned before the fallback: %v", pane.abandoned)
	}
	if len(local.built) != 1 || local.built[0].StateDir != filepath.Join(d.StateDir, fallbackDir) ||
		local.built[0].Profile.Runner != "claude" || local.built[0].Profile.Model != "opus" {
		t.Errorf("the headless executor was not built for the same profile under its own state root: %+v", local.built)
	}
	note, fell := w.StartFallback(handle)
	if !fell || !strings.Contains(note, "claude is waiting at its folder-trust dialog in /home/op/.herdr/worktrees") ||
		!strings.Contains(note, "headless claude runner") {
		t.Errorf("the fallback note does not say what happened: %q", note)
	}

	// Every operation on the fallen-back attempt reaches the local executor:
	// its own handle, and the reconciler's MINIMAL handle built from the
	// dispatch marker (naming herdr, pointing at herdr's state), which is
	// re-addressed at the local attempt's own state.
	minimal := &subprocess.JobHandle{JobID: spec.JobID, Attempt: 1, Executor: herdr.ExecutorName,
		Handle: map[string]any{"state": filepath.Join(d.StateDir, "herdr-state")}}
	for _, op := range []func(*subprocess.JobHandle) error{
		func(h *subprocess.JobHandle) error { _, err := w.Inspect(h, ""); return err },
		func(h *subprocess.JobHandle) error { _, err := w.CollectDetail(h); return err },
		func(h *subprocess.JobHandle) error { _, err := w.Cancel(h); return err },
		func(h *subprocess.JobHandle) error { return w.Dispose(h, subprocess.DisposeOptions{}) },
	} {
		if err := op(handle); err != nil {
			t.Fatal(err)
		}
		if err := op(minimal); err != nil {
			t.Fatal(err)
		}
	}
	if len(local.asked) != 8 || len(pane.asked) != 1 {
		t.Errorf("operations on the fallen-back attempt were not all local: local %v, herdr %v", local.asked, pane.asked)
	}
	if got := local.handles[len(local.handles)-1]; got.Executor != subprocess.ExecutorName ||
		got.Handle["state"] != handle.Handle["state"] {
		t.Errorf("the minimal handle reached the local executor as %+v, want it re-addressed at %v", got, handle.Handle["state"])
	}

	// A resumed run builds a fresh wrapper over the same state: the durable
	// marker sends its adopting Start straight to the headless executor.
	resumedPane := &fakePane{}
	resumedLocal := &fakeLocal{}
	resumed := withHeadlessFallback(resumedPane, d, resumedLocal.factory)
	if h, err := resumed.Start(spec); err != nil || h.Executor != subprocess.ExecutorName {
		t.Fatalf("the resume answered %+v, %v: want the fallen-back attempt adopted headless", h, err)
	}
	if resumedPane.starts != 0 {
		t.Error("the resume launched a herdr pane for an attempt that already fell back")
	}
	if _, fell := resumed.StartFallback(handle); fell {
		t.Error("a resumed adoption repeated the fallback's feed line")
	}
}

// Any other start failure is the herdr executor's, unchanged: no fallback,
// no abandonment.
func TestOnlyAPaneStartErrorFallsBack(t *testing.T) {
	t.Parallel()
	plain := errors.New("herdr worktree.create failed")
	pane := &fakePane{startErr: plain}
	local := &fakeLocal{}
	w := withHeadlessFallback(pane, paneDispatch(t, "claude"), local.factory)
	if _, err := w.Start(&subprocess.JobSpec{JobID: "j"}); !errors.Is(err, plain) {
		t.Fatalf("a plain start failure became %v", err)
	}
	refused := &subprocess.Refusal{Reason: subprocess.RefusedUnknown, Message: "held"}
	pane.startErr = refused
	if _, err := w.Start(&subprocess.JobSpec{JobID: "j"}); !errors.Is(err, refused) {
		t.Fatalf("a refusal became %v", err)
	}
	if local.starts != 0 || len(pane.abandoned) != 0 {
		t.Errorf("a non-pane failure fell back (local starts %d, abandoned %v)", local.starts, pane.abandoned)
	}
}

// The fallback is refused, loudly and with the pane's own diagnosis, when it
// cannot be made: a kind the headless executor has no runner for, or an
// attempt the herdr executor will not abandon.
func TestAFallbackThatCannotBeMadeKeepsThePaneDiagnosis(t *testing.T) {
	t.Parallel()
	pane := &fakePane{startErr: stuckAtTrust()}
	local := &fakeLocal{}
	w := withHeadlessFallback(pane, paneDispatch(t, "opencode"), local.factory)
	_, err := w.Start(&subprocess.JobSpec{JobID: "j"})
	if err == nil || !strings.Contains(err.Error(), "folder-trust dialog") || !strings.Contains(err.Error(), `no "opencode" runner`) {
		t.Errorf("an unsupported runner: %v", err)
	}
	if len(pane.abandoned) != 0 || local.starts != 0 {
		t.Error("a fallback with no runner to run on abandoned the pane anyway")
	}

	pane = &fakePane{startErr: stuckAtTrust(), abandonErr: errors.New("the agent is working")}
	d := paneDispatch(t, "claude")
	w = withHeadlessFallback(pane, d, local.factory)
	_, err = w.Start(&subprocess.JobSpec{JobID: "j"})
	if _, ok := herdr.AsPaneStartError(err); !ok || !strings.Contains(err.Error(), "the agent is working") {
		t.Errorf("a refused abandonment: %v", err)
	}
	if local.starts != 0 {
		t.Error("the headless runner started over an attempt herdr would not abandon")
	}
	if _, statErr := os.Stat(filepath.Join(d.StateDir, fallbackDir, fallbackMarker)); statErr == nil {
		t.Error("a fallback that never happened left the durable marker saying it did")
	}
}
