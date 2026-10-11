package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// A herdr pane that cannot start must not halt the run for a person (tick
// o35). The herdr executor already re-dispatches a stuck pane once, in the
// pane; when it still answers a *herdr.PaneStartError — an agent that came up
// and never became ready, with no prompt ever sent — this wrapper runs the
// SAME job on the headless local runner instead: the profile's own runner and
// model, launched the way `--no-herdr` launches every job. The never-prompted
// pane is abandoned first (herdr.AbandonUnstartedLaunch), which frees the
// attempt's branch for the local executor to cut again from the same base.
//
// The choice is durable: a marker beside the local executor's state (under
// the dispatch's own state directory) says this attempt fell back, so a
// resumed run's adopting Start goes straight to the local executor, and every
// later operation is routed by the handle's own executor name — the handle
// says which executor issued it. The feed says so through the reconciler's
// StartFallbacks hook (StageStartFellBack).

// fallbackDir is where the local executor keeps a fallen-back attempt's
// state: its own root under the dispatch's state directory, because the
// herdr and local executors key attempts identically and must not share one.
const fallbackDir = "headless-fallback"

// fallbackMarker is the durable record that this attempt fell back.
const fallbackMarker = "FELL_BACK"

// paneExecutor is the herdr half the wrapper needs: the executor, and the
// abandonment of a never-prompted launch (*herdr.Executor).
type paneExecutor interface {
	reconcile.Executor
	AbandonUnstartedLaunch(spec *subprocess.JobSpec, reason string) error
}

type herdrWithFallback struct {
	herdr    paneExecutor
	runner   string
	stateDir string
	newLocal func() (reconcile.Executor, error)

	mu    sync.Mutex
	local reconcile.Executor
	notes map[string]string
}

// withHeadlessFallback wraps a herdr executor built for d so that a pane that
// cannot start runs on the local executor reconcile.DefaultExecutor builds
// for the same dispatch, under its own state root.
func withHeadlessFallback(ex paneExecutor, d reconcile.Dispatch,
	local func(reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error)) *herdrWithFallback {
	ld := d
	ld.StateDir = filepath.Join(d.StateDir, fallbackDir)
	runner := ""
	if d.Profile != nil {
		runner = d.Profile.Runner
	}
	return &herdrWithFallback{
		herdr:    ex,
		runner:   runner,
		stateDir: ld.StateDir,
		newLocal: func() (reconcile.Executor, error) {
			executor, _, err := local(ld)
			return executor, err
		},
		notes: map[string]string{},
	}
}

func (w *herdrWithFallback) localExecutor() (reconcile.Executor, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.local != nil {
		return w.local, nil
	}
	local, err := w.newLocal()
	if err != nil {
		return nil, err
	}
	w.local = local
	return local, nil
}

func (w *herdrWithFallback) fellBack() bool {
	_, err := os.Stat(filepath.Join(w.stateDir, fallbackMarker))
	return err == nil
}

// Start starts the job in a herdr pane, and on a pane that cannot start runs
// it on the headless local runner instead.
func (w *herdrWithFallback) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if w.fellBack() {
		local, err := w.localExecutor()
		if err != nil {
			return nil, err
		}
		return local.Start(spec)
	}
	handle, err := w.herdr.Start(spec)
	pse, stuck := herdr.AsPaneStartError(err)
	if !stuck {
		return handle, err
	}
	if !slices.Contains(subprocess.KnownRunners(), w.runner) {
		return nil, fmt.Errorf("%w; the headless runner has no %q runner to fall back to", err, w.runner)
	}
	reason := fmt.Sprintf("the herdr pane could not start (%s)", pse.Error())
	if err := w.herdr.AbandonUnstartedLaunch(spec, "falling back to the headless "+w.runner+" runner"); err != nil {
		return nil, fmt.Errorf("%w; and the fallback to the headless runner could not free the attempt: %v", pse, err)
	}
	if err := os.MkdirAll(w.stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("%w; record the fallback: %v", pse, err)
	}
	if err := os.WriteFile(filepath.Join(w.stateDir, fallbackMarker), []byte(reason+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("%w; record the fallback: %v", pse, err)
	}
	local, err := w.localExecutor()
	if err != nil {
		return nil, fmt.Errorf("%w; build the headless runner: %v", pse, err)
	}
	handle, err = local.Start(spec)
	if err != nil {
		return nil, fmt.Errorf("%w; and the headless %s runner it fell back to did not start either: %v", pse, w.runner, err)
	}
	w.mu.Lock()
	w.notes[handle.JobID] = fmt.Sprintf("%s; the job runs on the headless %s runner instead (as --no-herdr "+
		"would run it), with no person needed", reason, w.runner)
	w.mu.Unlock()
	return handle, nil
}

// StartFallback is the reconciler's StartFallbacks hook: the feed line for a
// job this wrapper ran headless.
func (w *herdrWithFallback) StartFallback(handle *subprocess.JobHandle) (string, bool) {
	if handle == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	note, ok := w.notes[handle.JobID]
	return note, ok
}

// route is the executor that issued handle: the local one for a handle it
// names, herdr for every other.
func (w *herdrWithFallback) route(handle *subprocess.JobHandle) (reconcile.Executor, error) {
	if handle != nil && handle.Executor == subprocess.ExecutorName {
		return w.localExecutor()
	}
	return w.herdr, nil
}

func (w *herdrWithFallback) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	ex, err := w.route(handle)
	if err != nil {
		return nil, err
	}
	return ex.Inspect(handle, cursor)
}

func (w *herdrWithFallback) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	ex, err := w.route(handle)
	if err != nil {
		return nil, err
	}
	return ex.CollectDetail(handle)
}

func (w *herdrWithFallback) Cancel(handle *subprocess.JobHandle) (*subprocess.CancelAck, error) {
	ex, err := w.route(handle)
	if err != nil {
		return nil, err
	}
	return ex.Cancel(handle)
}

func (w *herdrWithFallback) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	ex, err := w.route(handle)
	if err != nil {
		return err
	}
	return ex.Dispose(handle, opts)
}

var (
	_ reconcile.Executor       = (*herdrWithFallback)(nil)
	_ reconcile.StartFallbacks = (*herdrWithFallback)(nil)
	_ paneExecutor             = (*herdr.Executor)(nil)
)
