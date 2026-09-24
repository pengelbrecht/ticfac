package cli

// The tracker-read tests, ported from ticks' cmd/tk/cmd/cloud_tk_test.go.
// ticks made the test binary double as tk through a self-exec sentinel; this
// binary does not ship the tracker, so the seam is the fake tk script in
// cloud_test.go — which keeps the subprocess REAL (the parent forks, the child
// reads the same fixtures `tk show --json` would) while costing no build step
// and depending on nothing installed.

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// The cloud commands read the tracker by running `tk show --json` and
// `tk list --json` as a subprocess (see cloudReadTracker in cloud.go). The
// fake tk answers from the fixtures the tests write, so the contract being
// exercised here is the one that matters: two invocations, whatever the repo
// holds, and a refusal that carries tk's own exit code.

// TestCloudTkAnswersLikeTk proves the fake tk substitution is load bearing:
// if the child ever stops behaving like tk, every cloud test that depends on
// it would fail for a reason that has nothing to do with the cloud commands,
// so it is asserted directly and once.
func TestCloudTkAnswersLikeTk(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudWaveRepo(t, "aaa")

	raw, err := cloudTkJSON(t.Context(), repo, "show", "aaa", "--json")
	if err != nil {
		t.Fatalf("tk show aaa --json: %v", err)
	}
	if !strings.Contains(string(raw), `"id":"aaa"`) {
		t.Errorf("tk show did not return the tick: %s", raw)
	}

	// A missing tick must come back as tk refusing, with tk's own exit code,
	// not as an unrunnable binary. That distinction is what keeps the exit
	// codes the cloud commands hand their users honest.
	_, err = cloudTkJSON(t.Context(), repo, "show", "nope", "--json")
	if err == nil {
		t.Fatal("tk show of a missing tick succeeded")
	}
	var tkErr *cloudTkError
	if !errors.As(err, &tkErr) {
		t.Fatalf("error %v is not a tk refusal", err)
	}
	if tkErr.code != exitNotFound {
		t.Errorf("tk exit code = %d, want %d", tkErr.code, exitNotFound)
	}
}

// TestCloudReadTrackerBatchesTheList counts the subprocesses one tracker read
// costs. It must be two — one show, one list — regardless of how many ticks
// the epic has, because a call per tick is the failure mode this whole
// approach exists to avoid.
func TestCloudReadTrackerBatchesTheList(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudWaveRepo(t, "aaa", "bbb", "ccc", "ddd", "eee")

	inner := cloudTkBinary
	var calls int
	cloudTkBinary = func() (string, []string, error) {
		calls++
		return inner()
	}
	t.Cleanup(func() { cloudTkBinary = inner })

	tracker, err := cloudReadTracker(t.Context(), repo, "epic1")
	if err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	if calls != 2 {
		t.Errorf("a tracker read cost %d tk invocations, want 2 (one show, one list)", calls)
	}
	if !tracker.isEpic() {
		t.Error("epic1 did not read back as an epic")
	}
	for _, id := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		if !tracker.isDescendant(id) {
			t.Errorf("%s did not read back as a descendant of epic1", id)
		}
	}
	if got := len(tracker.epicPaths()); got != 6 {
		t.Errorf("epicPaths returned %d paths, want 6 (the epic and its five ticks)", got)
	}
}

// TestCloudTrackerReadsTicksOwnedByAnyone guards the `--all` on the list call.
// `tk list` shows only the invoking user's ticks by default, and an epic's
// descendants are owned by whoever filed them — a worker container owns what
// it filed. Without --all the descendant walk would silently lose them and a
// legitimate wave would be refused as "outside the epic".
func TestCloudTrackerReadsTicksOwnedByAnyone(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudWaveRepo(t, "aaa")
	writeCloudTickFixture(t, repo, cloudTickFixture{
		ID: "zzz", Title: "Tick zzz", Type: "task",
		Parent: "epic1", Owner: "someone-else@example.com", CreatedBy: "someone-else@example.com",
	})

	tracker, err := cloudReadTracker(t.Context(), repo, "epic1")
	if err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	if !tracker.isDescendant("zzz") {
		t.Error("a tick owned by another user was invisible to the tracker read")
	}
}

// TestCloudRunExitCodesFromTheTkRead pins how `cloud run` surfaces a tracker
// read that refused: prepareCloudSubmission reads the epic before anything is
// pushed, so a lookup failure or a non-epic must cost no factory call and no
// push — and the refusal must name what the operator typed.
func TestCloudRunExitCodesFromTheTkRead(t *testing.T) {
	stubCloudTk(t)
	setupCloudWaveRepo(t, "aaa")
	endpoint, requests := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_should_not_start"}}
	})
	configureCloudFactory(t, endpoint)

	for name, tc := range map[string]struct {
		args []string
		says string
	}{
		// tk ran and refused the epic lookup: the epic is not in this checkout.
		"missing epic": {[]string{"cloud", "run", "nope"}, "nope"},
		// tk answered, and the answer is a task, so it is not a submittable epic.
		"not an epic": {[]string{"cloud", "run", "aaa"}, "not an epic"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runCloudArgs(t, tc.args)
			if code == exitSuccess {
				t.Fatalf("%v was accepted, want a refusal", tc.args)
			}
			if code != exitGeneric {
				t.Errorf("exit code = %d, want %d — refusal was %q", code, exitGeneric, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.says) {
				t.Errorf("refusal %q does not mention %q", stderr.String(), tc.says)
			}
		})
	}

	if len(*requests) != 0 {
		t.Errorf("refusals made %d factory call(s); the tracker read runs before the network", len(*requests))
	}
}

// TestCloudRunOnAnUnrunnableTkSaysSo is cloudTkJSON's third outcome.
//
// A tk that cannot be executed is an environment fault, not a missing epic. It
// must exit 1, and the message must name the binary that could not be run AND
// the arguments it would have been run with — a bare non-zero exit, or worse a
// "no such epic", tells a human nothing they can act on.
func TestCloudRunOnAnUnrunnableTkSaysSo(t *testing.T) {
	stubCloudTk(t)
	setupCloudWaveRepo(t, "aaa")
	endpoint, requests := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_should_not_start"}}
	})
	configureCloudFactory(t, endpoint)

	missing := filepath.Join(t.TempDir(), "tk-that-is-not-there")
	inner := cloudTkBinary
	cloudTkBinary = func() (string, []string, error) { return missing, nil, nil }
	t.Cleanup(func() { cloudTkBinary = inner })

	code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"})
	if code == exitSuccess {
		t.Fatal("cloud run submitted an epic whose tracker it could not read")
	}
	if code != exitGeneric {
		t.Errorf("exit code = %d, want %d — an unrunnable tk is an environment fault, not a missing epic (%d)",
			code, exitGeneric, exitNotFound)
	}
	for _, want := range []string{missing, "tk show epic1 --json"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("refusal %q does not name %q, so a human cannot tell which invocation failed", stderr.String(), want)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("an unreadable tracker cost %d factory call(s); it must cost none", len(*requests))
	}
}
