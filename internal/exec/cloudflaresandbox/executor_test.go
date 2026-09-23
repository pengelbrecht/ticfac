package cloudflaresandbox

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Start and Inspect over the door, and the three operations the door does
// not carry, refused. Every test drives the executor against the fake door,
// because the property this package owns is the CONTRACT, and a contract
// exercisable only against a deployed Worker is a contract nobody tests.

// short: an httptest door and one state directory; no network, no container.
func TestStartReturnsAHandleWithoutBlocking(t *testing.T) {
	h := newHarness(t)
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Executor != ExecutorName {
		t.Errorf("handle.Executor is %q, want %q", handle.Executor, ExecutorName)
	}
	if handle.JobID != h.spec.JobID {
		t.Errorf("handle.JobID is %q, want %q: the door mints the job id from the credential", handle.JobID, h.spec.JobID)
	}
	if handle.Attempt != 1 {
		t.Errorf("handle.Attempt is %d, want 1", handle.Attempt)
	}
	if handle.Handle == nil {
		t.Fatal("handle.Handle is absent: there is nothing to re-address")
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if want := "r1-keh-1"; payload.Sandbox != want {
		t.Errorf("payload.Sandbox is %q, want %q (the container is named by the attempt's identity)", payload.Sandbox, want)
	}
	if payload.ProcessID == nil || *payload.ProcessID == "" {
		t.Error("payload.ProcessID is empty: the dispatch was confirmed to a process")
	}
	if payload.TickID != "keh" {
		t.Errorf("payload.TickID is %q, want keh", payload.TickID)
	}

	// The request is the door's own documented body, closed on both sides:
	// one field this client stopped sending is a drift this test exists to
	// catch, the same way a field the route stopped reading is.
	body := h.door.lastStartBody()
	for field, want := range map[string]any{
		"epic":      "xte",
		"tick_id":   "keh",
		"attempt":   float64(1),
		"role":      "implement-tick",
		"base_ref":  "epic/xte",
		"base_sha":  "0123456789abcdef0123456789abcdef01234567",
		"write_ref": h.spec.Source.WriteRef,
		"title":     "A cloudflare-sandbox executor that returns a handle, not a result",
	} {
		if got := body[field]; got != want {
			t.Errorf("the start body's %q is %v, want %v", field, got, want)
		}
	}
	if auth := h.door.lastAuthorization(); auth != "Bearer run-r1-token" {
		t.Errorf("the door saw credential %q, want the run's own gateway token", auth)
	}

	// A handle, not a result: the attempt is STILL RUNNING after Start came
	// back, which is the whole shape — Start waited for a confirmed dispatch,
	// never for the tick's work.
	h.running("keh")
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("after Start returned, the attempt reads %q, want %q: an executor that blocks would read a terminal state",
			status.State, subprocess.StateRunning)
	}
	if status.Terminal {
		t.Error("a just-started attempt is terminal: Start blocked until the work finished, the exact thing nothing-waiting forbids")
	}
}

// short: an httptest door and one state directory.
func TestStartAdoptsALiveIdentityWithoutASecondBoot(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	first, err := h.start("keh")
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if h.door.startCount() != 1 {
		t.Fatalf("the door saw %d starts, want 1", h.door.startCount())
	}

	// The same identity again: the record plus the live container is an
	// adoption, and no second boot round trip is spent to learn it.
	second, err := h.start("keh")
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if h.door.startCount() != 1 {
		t.Errorf("the door saw %d starts after a redispatch of a live identity, want 1: a second boot is a rival",
			h.door.startCount())
	}
	if second.JobID != first.JobID || second.Attempt != first.Attempt {
		t.Errorf("the adopted handle is %s attempt %d, want the SAME identity as the first (%s attempt %d)",
			second.JobID, second.Attempt, first.JobID, first.Attempt)
	}
	a, err := local(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := local(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Sandbox != b.Sandbox {
		t.Errorf("the adopted handle names container %q, want %q", b.Sandbox, a.Sandbox)
	}
}

// short: an httptest door and one state directory.
func TestStartRefusesASettledIdentity(t *testing.T) {
	h := newHarness(t)
	h.settled("keh", subprocess.StateSucceeded)
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start over an identity that already settled was accepted")
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		t.Fatalf("the error is not a typed refusal: %v", err)
	}
	if refusal.Reason != subprocess.RefusedSettled {
		t.Errorf("refusal reason is %q, want %q: a retry is a new attempt number, not this one again",
			refusal.Reason, subprocess.RefusedSettled)
	}
	if h.door.startCount() != 0 {
		t.Errorf("the door saw %d starts, want 0: a settled identity is refused before any boot", h.door.startCount())
	}
}

// short: an httptest door and one state directory.
func TestStartHoldsAnIdentityItCannotAddress(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// The container is gone from the door's answer — a statement about the
	// observer. The local record is durable evidence the dispatch once
	// landed, so the attempt is HELD, never redispatched.
	h.door.setStatus("keh", 1, doorStatus{state: subprocess.StateLost, terminal: false})
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start of an unaddressable recorded attempt was accepted")
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		t.Fatalf("the error is not a typed refusal: %v", err)
	}
	if refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("refusal reason is %q, want %q: nobody can say whether it is running, which is not nothing-is",
			refusal.Reason, subprocess.RefusedUnknown)
	}
	if h.door.startCount() != 1 {
		t.Errorf("the door saw %d starts, want 1: a held identity must not boot a second container under the same name",
			h.door.startCount())
	}
}

// short: an httptest door and one state directory.
func TestStartHoldsAnUnreadableRecord(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	dir := h.ex.stateDirFor(h.spec.JobID, 1)
	if err := os.WriteFile(filepath.Join(dir, fileAttempt), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The record EXISTS and this leg cannot read it: a handle a later leg
	// cannot address, held for a person rather than redispatched behind an
	// unreadable record.
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start over an unreadable attempt record was accepted")
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		t.Fatalf("the error is not a typed refusal: %v", err)
	}
	if refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("refusal reason is %q, want %q", refusal.Reason, subprocess.RefusedUnknown)
	}
	if h.door.startCount() != 1 {
		t.Errorf("the door saw %d starts, want 1: an unreadable record is a hold, not a licence to boot", h.door.startCount())
	}
}

// short: an httptest door and two state directories.
func TestStartAdoptsThroughTheDoorOnAFreshHost(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// The fresh-clone case: the previous incarnation's state is gone, the
	// door is the only thing that still knows the attempt — and a start
	// under the same identity must find the RUNNING one, not boot a rival.
	fresh := newHarnessAt(t, h.door)
	handle, err := fresh.start("keh")
	if err != nil {
		t.Fatalf("fresh-host Start: %v", err)
	}
	if handle.JobID != h.spec.JobID {
		t.Errorf("the fresh host minted handle %s, want %s", handle.JobID, h.spec.JobID)
	}
	if h.door.startCount() != 2 {
		t.Errorf("the door saw %d starts, want 2 (one boot, one adoption round trip)", h.door.startCount())
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if want := "r1-keh-1"; payload.Sandbox != want {
		t.Errorf("the adopted handle names container %q, want %q", payload.Sandbox, want)
	}
}

// The acceptance criterion, literally: a handle is re-queried for state AFTER
// the process that created it is gone. The "process that created it" is the
// executor instance — everything it held in memory is what a fresh one must
// not need — and the door is re-addressed BY IDENTITY, from the handle and
// nothing else.
//
// short: an httptest door and two state directories.
func TestInspectRequeriesStateAfterTheCreatingProcessIsGone(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A fresh executor — a different process, a different state root, holding
	// nothing the first one held but the handle.
	other := newHarnessAt(t, h.door)

	status, err := other.ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("a fresh process Inspect of the first one's handle: %v", err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("the fresh process read state %q, want %q", status.State, subprocess.StateRunning)
	}
	if status.JobID != h.spec.JobID {
		t.Errorf("the status answers for %q, want %q", status.JobID, h.spec.JobID)
	}
	if other.door.startCount() != 1 {
		t.Errorf("Inspect dispatched: %d starts, want 1 — the state route reads, it never boots", other.door.startCount())
	}

	// The handle keeps answering as the attempt's own state changes: the
	// door is asked again, by the same identity, and the terminal answer
	// arrives with its exit observation.
	h.settled("keh", subprocess.StateSucceeded)
	status, err = other.ex.Inspect(handle, "some cursor")
	if err != nil {
		t.Fatalf("Inspect of a finished attempt: %v", err)
	}
	if !status.Terminal || status.State != subprocess.StateSucceeded {
		t.Errorf("the finished attempt reads state %q terminal=%v, want succeeded terminal=true", status.State, status.Terminal)
	}
	if len(status.Observations) == 0 {
		t.Error("a terminal status carries no observations: the exit code is the whole result this layer has")
	}
}

// The minimal handle — the {"state": …} shape the reconciler's adoption path
// synthesizes from an attempt record — resolves the identity from the record
// and asks the door anyway: the record is the authority the frozen copy is
// the copy of.
//
// short: an httptest door and two state directories.
func TestInspectResolvesAMinimalHandleFromTheAttemptRecord(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dir := h.ex.stateDirFor(h.spec.JobID, 1)

	// A FRESH process holding only the minimal handle: no payload fields, no
	// in-memory anything, just the state directory the record lives in.
	other := newHarnessAt(t, h.door)
	minimal := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         h.spec.JobID,
		Attempt:       1,
		Executor:      ExecutorName,
		Handle:        map[string]any{"state": dir},
	}
	status, err := other.ex.Inspect(minimal, "")
	if err != nil {
		t.Fatalf("Inspect of a minimal handle: %v", err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("the minimal handle read state %q, want %q", status.State, subprocess.StateRunning)
	}
	if other.door.startCount() != 1 {
		t.Errorf("Inspect dispatched: %d starts, want 1", other.door.startCount())
	}
}

// short: an httptest door and one state directory.
func TestInspectRefusesAHandleWithNoIdentity(t *testing.T) {
	h := newHarness(t)
	_, err := h.ex.Inspect(&subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         h.spec.JobID,
		Attempt:       1,
		Executor:      ExecutorName,
		Handle:        map[string]any{"detail": "nothing addressable"},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "neither a state directory nor a tick id") {
		t.Fatalf("a handle with no identity must be refused as unaddressable, got %v", err)
	}
}

// short: an httptest door and one state directory.
func TestInspectRefusesAnotherExecutorsHandle(t *testing.T) {
	h := newHarness(t)
	_, err := h.ex.Inspect(&subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         h.spec.JobID,
		Attempt:       1,
		Executor:      "local-subprocess",
		Handle:        map[string]any{"state": "/nowhere"},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "local-subprocess") {
		t.Fatalf("another executor's handle must be refused by name, got %v", err)
	}
}

// short: an httptest door and one state directory.
func TestTheDoorMintingTheWrongJobIsRefused(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	spec := h.spec // the job id the RECONCILER owns, frozen before the door drifts
	h.door.setRunID("somebody-else")
	_, err := h.ex.Start(spec)
	if err == nil || !strings.Contains(err.Error(), "the door minted handle") {
		t.Fatalf("a door minting a handle for another run's job must be refused, got %v", err)
	}
}

// short: an httptest door and one state directory.
func TestTheStatusAnsweringForAnotherJobIsRefused(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.door.setRunID("somebody-else")
	if _, err := h.ex.Inspect(handle, ""); err == nil || !strings.Contains(err.Error(), "the door answered for") {
		t.Fatalf("a status answering for another run's attempt must be refused, got %v", err)
	}
}

// Tick avx's rule, held where the route's own header says it lives: in the
// CLIENT's error handling. A door this executor cannot reach is a transport
// error, never a `lost` — reading an outage as absence is how an attempt gets
// written off while its container is still running.
//
// short: a closed localhost port; no container.
func TestAnUnreachableDoorIsAnErrorNotLost(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	dead := newHarnessAt(t, h.door)
	dead.door.server.Close()
	status, err := dead.ex.Inspect(handle, "")
	if err == nil {
		t.Fatal("an unreachable door answered with a status")
	}
	if status != nil {
		t.Fatalf("an unreachable door answered with status %q: unreachable is not absent", status.State)
	}
	if _, isDoor := AsDoorError(err); isDoor {
		t.Errorf("a transport failure was typed as the door's own refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("the error does not say the door was unreachable: %v", err)
	}

	// The same rule on Start: a transport blip fails the dispatch, it does
	// not mint a lost-and-gone verdict on the attempt.
	if _, err := dead.start("keh"); err == nil {
		t.Fatal("a Start through an unreachable door was accepted")
	} else if strings.Contains(err.Error(), subprocess.StateLost) {
		t.Errorf("a transport failure read as `lost`: %v", err)
	}
}

// short: an httptest door and one state directory.
func TestDoorRefusalsArriveTyped(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name   string
		status int
		class  string
		detail string
	}{
		{"run_token_required", http.StatusUnauthorized, "run_token_required", "no credential at all"},
		{"run_token_unknown", http.StatusUnauthorized, "run_token_unknown", "a credential this deployment never minted"},
		{"run_token_revoked", http.StatusForbidden, "run_token_revoked", "the run's kill switch reached this door"},
		{"lease_lost", http.StatusConflict, "lease_lost", "no live lease"},
		{"sandbox_dispatch_not_wired", http.StatusServiceUnavailable, "sandbox_dispatch_not_wired", "no binding"},
	} {
		h.door.refuseWith(tc.status, tc.class, tc.detail)
		_, err := h.start("another")
		door, ok := AsDoorError(err)
		if !ok {
			t.Errorf("%s: the refusal did not arrive typed: %v", tc.name, err)
			continue
		}
		if door.Class != tc.class {
			t.Errorf("%s: class is %q, want %q", tc.name, door.Class, tc.class)
		}
		if door.Status != tc.status {
			t.Errorf("%s: status is %d, want %d", tc.name, door.Status, tc.status)
		}
		if door.Detail != tc.detail {
			t.Errorf("%s: detail is %q, want %q", tc.name, door.Detail, tc.detail)
		}
	}

	// A body the door would never send keeps the status and says so: the
	// refusal is still the door's, and a status a caller cannot classify is
	// surfaced, not swallowed.
	h.door.answerGarbage(http.StatusInternalServerError)
	_, err := h.start("another")
	door, ok := AsDoorError(err)
	if !ok {
		t.Fatalf("an unclassifiable refusal did not arrive typed: %v", err)
	}
	if door.Class != "unreadable_refusal" {
		t.Errorf("class is %q, want unreadable_refusal", door.Class)
	}
}

// The three operations the door does not carry: refused, typed, and naming
// the open decision rather than half-acting. Collect and cancel cross this
// boundary only when the door's own header says where they live — and until
// then a run that reaches one of them stops honestly.
//
// short: an httptest door and one state directory.
// short: the fake door and one state directory.
//
// Cancel and dispose are refused with the DECIDED reasons (tick xev): the
// credential a container holds is the run's own token, so there is no
// per-attempt dispatch to revoke, and the container's teardown belongs to
// the factory. Collect is no longer refused — it is the Go side's, from git
// (collect_test.go) — so the trio this test used to walk is a pair now.
func TestCancelAndDisposeRefuseWithTheDecidedReasons(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, tc := range []struct {
		name   string
		call   func() error
		reason string
	}{
		{"cancel", func() error {
			_, err := h.ex.Cancel(handle)
			return err
		}, RefusedCancelOwnedByFactory},
		{"dispose", func() error {
			return h.ex.Dispose(handle, subprocess.DisposeOptions{})
		}, RefusedNothingLocalToDispose},
	} {
		err := tc.call()
		refusal, ok := subprocess.AsRefusal(err)
		if !ok {
			t.Errorf("%s did not arrive as a typed refusal: %v", tc.name, err)
			continue
		}
		if refusal.Reason != tc.reason {
			t.Errorf("%s refused with %q, want %q", tc.name, refusal.Reason, tc.reason)
		}
		if !strings.Contains(refusal.Message, "tick xev") {
			t.Errorf("%s's refusal does not name the decision it is held for: %s", tc.name, refusal.Message)
		}
	}
}

// short: no door at all.
//
// "Unconfigured" is CONSTRUCTED here, not inherited from the host. New
// fills a missing FactoryURL or Token from TICKS_FACTORY_URL and
// TICKS_FACTORY_TOKEN, and inside a factory container both ARE set in the
// environment — the container is a run — so the constructor finds them and
// builds a client, and the test would report the host it runs on, not the
// tree. Clearing the variables with t.Setenv (which forbids t.Parallel) is
// what makes this assert the tree's behaviour on a laptop and in the
// container alike.
func TestNewRefusesAnUnconfiguredDoor(t *testing.T) {
	t.Setenv("TICKS_FACTORY_URL", "")
	t.Setenv("TICKS_FACTORY_TOKEN", "")
	if _, err := New(Options{Token: "t", StateDir: t.TempDir()}); err == nil {
		t.Error("a client with no factory to ask was built")
	}
	if _, err := New(Options{FactoryURL: "https://factory.example.com", StateDir: t.TempDir()}); err == nil {
		t.Error("a client with no run credential was built")
	}
}

// Appendix A #7: a write that silently did not land must not look like a job
// somebody can address. The record is what a later leg re-addresses the
// attempt through; a start whose record never landed returns no handle.
//
// short: an httptest door and one state directory.
func TestARecordThatDidNotLandStopsTheHandle(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	dropped := 0
	h.ex.opts.writeFile = func(path string, data []byte, perm os.FileMode) error {
		dropped++
		return nil // writes "succeed" and land nowhere
	}
	if _, err := h.start("keh"); err == nil {
		t.Fatal("a start whose record did not land returned a handle")
	}
	if dropped == 0 {
		t.Error("the injected writer was never asked to write: the record path changed")
	}
	// The dispatch DID reach the door — the failure is the record, and it
	// must stop the handle after it, not the dispatch before it.
	if h.door.startCount() != 1 {
		t.Errorf("the door saw %d starts, want 1: the record failure is after the dispatch, not before it", h.door.startCount())
	}
	// And no later leg can find the attempt: nothing was recorded.
	dir := h.ex.stateDirFor(h.spec.JobID, 1)
	if _, err := os.Stat(filepath.Join(dir, fileAttempt)); !os.IsNotExist(err) {
		t.Errorf("an attempt record exists at %s despite the dropped write: %v", dir, err)
	}
}

// short: two state directories, no door.
func TestStateDirsSeparateIdentities(t *testing.T) {
	h := newHarness(t)
	a := h.ex.stateDirFor("run-r1/tick-a/attempt-1", 1)
	b := h.ex.stateDirFor("run-r1/tick-b/attempt-1", 1)
	if a == b {
		t.Errorf("two attempts of different jobs share one state directory %q", a)
	}
	if !strings.HasPrefix(a, h.state+string(filepath.Separator)) {
		t.Errorf("state directory %q is not under the state root %q", a, h.state)
	}
}

// errors.Is/As plumbing sanity: a door refusal wraps nothing and is itself.
//
// short: no I/O.
func TestAsDoorErrorRejectsForeignErrors(t *testing.T) {
	if _, ok := AsDoorError(errors.New("plain")); ok {
		t.Error("a plain error was typed as a door refusal")
	}
	if _, ok := AsDoorError(nil); ok {
		t.Error("nil was typed as a door refusal")
	}
}
