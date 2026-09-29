package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// failedSettleExecutor answers a1's first terminal status as a failure with
// the executor's own last word on it — the shape a sandbox door answers for a
// worker that died in its boot.
type failedSettleExecutor struct {
	Executor
	tick string
	try  int
}

func (e *failedSettleExecutor) Inspect(h *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := e.Executor.Inspect(h, cursor)
	if err != nil || status == nil || !status.Terminal || e.tick != "a1" || e.try != 1 {
		return status, err
	}
	failed := *status
	failed.State = subprocess.StateFailed
	failed.Observations = append(append([]subprocess.Observation{}, status.Observations...), subprocess.Observation{
		Kind: subprocess.ObsExited, Detail: "the container's work process exited 3 (the checkout failed)"})
	return &failed, nil
}

// A failed settle says what the executor last observed (epic hn6: r5i try 2
// "settled as failed" and nothing else, while its exit code named the step
// of the container's boot that died).
func TestAFailedSettleCarriesTheExecutorsLastWord(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	options := f.options(f.Repo, fixtureOptions{})
	options.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		inner, substrate, err := f.newExecutor(d)
		if err != nil {
			return nil, substrate, err
		}
		return &failedSettleExecutor{Executor: inner, tick: d.TickID, try: d.Try}, substrate, nil
	}
	r, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.RunProtected(t.Context())
	for _, e := range r.Journal() {
		if e.Tick == "a1" && e.Stage == StageWaiting && strings.HasPrefix(e.Detail, "settled as failed") {
			if !strings.Contains(e.Detail, "exited 3 (the checkout failed)") {
				t.Errorf("the failed settle line does not carry the executor's last word: %s", e.Detail)
			}
			return
		}
	}
	t.Fatalf("no failed settle line for a1:\n%s", journalText(r))
}
