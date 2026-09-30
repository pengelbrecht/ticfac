package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Under rollout_active_grace_period (cloudflare/wrangler.toml, PR #150) a
// rollout leaves a live run's containers on the previous image, stays
// `progressing` until the run lets them go, and the application record keeps
// naming the previous image until then. These pin that a deploy calls that
// rollout what it is — serving new instances, held open by named runs — and
// still refuses to call anything short of that a success.

const heldRun = "run_3f034e683faf4fdf9f6c0952fc158524"

func intp(n int) *int { return &n }

// liveInstances is the shape `wrangler containers instances --json` printed on
// 2026-09-30, with a run holding two instances on version 57, one new instance
// on 58 and a Durable Object with no instance.
func liveInstances() []containerInstance {
	return []containerInstance{
		{ID: "a", Name: heldRun, State: "running", Version: intp(57)},
		{ID: "b", Name: heldRun + "-7uv-2", State: "running", Version: intp(57)},
		{ID: "c", Name: "run_8511bc66fe444bfaa4b5c9d6cd2073b4-1", State: "running", Version: intp(58)},
		{ID: "d", Name: "run_911b556fce394004837695b3196e3925-3gk-3", State: "inactive"},
	}
}

func progressingRollout(digest string) rolloutRecord {
	var r rolloutRecord
	r.ID = "rollout-1"
	r.Status = "progressing"
	r.TargetVersion = 58
	r.Target.Image = "registry.cloudflare.com/acct/ticks-orchestrator@" + digest
	r.Progress.UpdatedInstances = 5
	r.Progress.TotalInstances = 7
	return r
}

func TestJudgeHeldRollout(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("2", 64)

	held, why := judgeHeldRollout(progressingRollout(digest), digest, liveInstances())
	if why != "" {
		t.Fatalf("a rollout held only by a live run was not accepted: %s", why)
	}
	if !reflect.DeepEqual(held.Runs, []string{heldRun}) || held.Held != 2 {
		t.Errorf("held = %+v, want 2 instances held by %s", held, heldRun)
	}

	for name, tc := range map[string]struct {
		mutate func(*rolloutRecord, *[]containerInstance)
		want   string
	}{
		"an old instance no run holds": {
			func(_ *rolloutRecord, in *[]containerInstance) {
				*in = append(*in, containerInstance{ID: "e", State: "running", Version: intp(57)})
			}, "held by no live run",
		},
		"an old instance that is not running": {
			func(_ *rolloutRecord, in *[]containerInstance) {
				(*in)[0].State = "provisioning"
			}, "held by no live run",
		},
		"another deploy's image": {
			func(r *rolloutRecord, _ *[]containerInstance) {
				r.Target.Image = "registry.cloudflare.com/acct/ticks-orchestrator@sha256:" + strings.Repeat("9", 64)
			}, "different image",
		},
		"failing instances": {
			func(r *rolloutRecord, _ *[]containerInstance) { r.Health.Instances.Failed = 1 },
			"failing",
		},
		"no new instance yet": {
			func(r *rolloutRecord, in *[]containerInstance) {
				r.Progress.UpdatedInstances = 0
				*in = (*in)[:2]
			}, "no instance has come up",
		},
		"a rollout another replaced": {
			func(r *rolloutRecord, _ *[]containerInstance) { r.Status = "replaced" },
			"not progressing",
		},
	} {
		r, in := progressingRollout(digest), liveInstances()
		tc.mutate(&r, &in)
		if _, why := judgeHeldRollout(r, digest, in); !strings.Contains(why, tc.want) {
			t.Errorf("%s: why = %q, want it to say %q", name, why, tc.want)
		}
	}
}

// rolloutsAPI serves one rollout and records how it was asked.
type rolloutsAPI struct {
	mu       sync.Mutex
	paths    []string
	auth     []string
	rollout  string
	requests int
}

func newRolloutsAPI(t *testing.T, targetDigest string) (*rolloutsAPI, *httptest.Server) {
	t.Helper()
	api := &rolloutsAPI{rollout: fmt.Sprintf(`{"success":true,"result":{"id":"rollout-1","status":"progressing",`+
		`"target_version":58,"current_version":57,`+
		`"target_configuration":{"image":"registry.cloudflare.com/acct/ticks-orchestrator@%s"},`+
		`"health":{"errors":[],"instances":{"active":3,"failed":0}},`+
		`"progress":{"updated_instances":5,"total_instances":7}}}`, targetDigest)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.paths = append(api.paths, r.URL.Path)
		api.auth = append(api.auth, r.Header.Get("Authorization"))
		api.requests++
		api.mu.Unlock()
		_, _ = w.Write([]byte(api.rollout))
	}))
	t.Cleanup(srv.Close)
	return api, srv
}

const liveInstancesJSON = `[
 {"id":"a","name":"` + heldRun + `","state":"running","location":"cdg15","version":57,"created":"2026-09-30T16:07:26Z"},
 {"id":"b","name":"` + heldRun + `-7uv-2","state":"running","location":"lhr21","version":57,"created":"2026-09-30T16:07:26Z"},
 {"id":"c","name":"run_8511bc66fe444bfaa4b5c9d6cd2073b4-1","state":"running","location":"lhr19","version":58,"created":"2026-09-30T16:30:00Z"},
 {"id":"d","name":"run_911b556fce394004837695b3196e3925-3gk-3","state":"inactive","location":null,"version":null,"created":"2026-09-30T13:39:46Z"}
]`

// The deploy that used to fail: the application record never moves to the
// new image inside the wait because a live run holds two instances, yet a run
// started now boots the new one. It is a confirmed deploy that names the run.
func TestDeployConfirmsARolloutHeldOpenByALiveRun(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_WRANGLER_ROLLOUT_STUCK", "1")
	t.Setenv("FAKE_WRANGLER_ROLLOUT_ACTIVE", "1")
	t.Setenv("FAKE_WRANGLER_INSTANCES", liveInstancesJSON)
	digest := "sha256:" + strings.Repeat("2", 64)
	api, srv := newRolloutsAPI(t, digest)

	var out bytes.Buffer
	opts := h.rolloutOptions()
	opts.Out = &out
	opts.cloudflareAPIBase = srv.URL
	opts.heldCheckEvery = time.Nanosecond

	result, err := Deploy(context.Background(), opts)
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, out.String())
	}
	if !result.RolloutConfirmed || result.ImageDigest != digest {
		t.Errorf("result = confirmed %v digest %q, want the new image confirmed", result.RolloutConfirmed, result.ImageDigest)
	}
	if !reflect.DeepEqual(result.RolloutHeldBy, []string{heldRun}) {
		t.Errorf("RolloutHeldBy = %v, want [%s]", result.RolloutHeldBy, heldRun)
	}
	for _, want := range []string{
		"serves sha256:222222222222 to new instances",
		"held by live run(s): " + heldRun,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the deploy's output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "fake-api-token") {
		t.Errorf("the deploy printed wrangler's API token:\n%s", out.String())
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.paths) == 0 || api.paths[0] != "/accounts/acct/containers/applications/app-1/rollouts/rollout-1" ||
		api.auth[0] != "Bearer fake-api-token" {
		t.Errorf("rollouts API asked %v with %v, want the active rollout with wrangler's token", api.paths, api.auth)
	}
}

// An instance on the old image that no run holds means the rollout has not
// reached every instance it can: that is still not a confirmed deploy, and the
// failure says why.
func TestDeployDoesNotConfirmARolloutAnUnheldInstanceIsStillOn(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_WRANGLER_ROLLOUT_STUCK", "1")
	t.Setenv("FAKE_WRANGLER_ROLLOUT_ACTIVE", "1")
	t.Setenv("FAKE_WRANGLER_INSTANCES", strings.Replace(liveInstancesJSON, `"name":"`+heldRun+`",`, `"name":null,`, 1))
	_, srv := newRolloutsAPI(t, "sha256:"+strings.Repeat("2", 64))

	opts := h.rolloutOptions()
	opts.rolloutTimeout = 50 * time.Millisecond
	opts.rolloutExtension = time.Millisecond
	opts.cloudflareAPIBase = srv.URL
	opts.heldCheckEvery = time.Nanosecond

	_, err := Deploy(context.Background(), opts)
	var rollout *RolloutError
	if !errors.As(err, &rollout) {
		t.Fatalf("Deploy error = %T (%v), want a *RolloutError", err, err)
	}
	if !strings.Contains(rollout.Reason, "held by no live run") {
		t.Errorf("Reason = %q, want it to say an old instance is held by no live run", rollout.Reason)
	}
}

// A deploy that pushed nothing while an earlier deploy's rollout is still held
// open must confirm that earlier deploy's image — the rollout's target — not
// the older image the application record still names.
func TestAnIdempotentDeployConfirmsTheHeldRolloutsTarget(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("FAKE_WRANGLER_STATE", stateDir)
	t.Setenv("FAKE_WRANGLER_LOG", stateDir+"/wrangler.log")
	t.Setenv("FAKE_WRANGLER_ROLLOUT_ACTIVE", "1")
	t.Setenv("FAKE_WRANGLER_INSTANCES", liveInstancesJSON)
	target := "sha256:" + strings.Repeat("2", 64)
	_, srv := newRolloutsAPI(t, target)

	w := &wrangler{bin: testdataDir + "/fake-wrangler.sh", dir: t.TempDir()}
	var out bytes.Buffer
	outcome, err := confirmContainerRollout(context.Background(), w, &out,
		"Image already exists remotely, skipping push\n", time.Minute, time.Millisecond, 0, false,
		rolloutAPI{base: srv.URL, every: time.Nanosecond})
	if err != nil {
		t.Fatalf("confirmContainerRollout: %v\n%s", err, out.String())
	}
	if outcome.Digest != target || !outcome.Confirmed || len(outcome.HeldBy) != 1 {
		t.Errorf("outcome = %+v, want the held rollout's target %s confirmed", outcome, target)
	}
}
