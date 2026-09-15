package reconcile

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// This file guards the incident from the ticks pwp run (ticfac tick 4ys):
// `ticfac settle --release "<who>"` recorded the string verbatim as
// released_by in the decision .ticfac COMMITS to the target repository's epic
// branch — and a public repository's rules forbid operator identifiers in
// tracked files, so the epic's PR failed its own repo's guard.
//
// The fix has two halves, both tested here:
//
//   - an operator identity handed to a ticfac command never lands verbatim in
//     a committed record: the decision carries a stable PSEUDONYMOUS handle,
//     and the real value survives in run-local state outside the repository;
//   - digestOf (guards.go) is a FULL sha256, not a sha256 truncated to 32 hex
//     characters — 32 hex is exactly the shape of a cloud account id, and a
//     label saying sha256 over half of one is what tripped the public-repo
//     account detector 60 times on one epic.

// TestAReleaseNeverRecordsTheOperatorIdentity is the acceptance criterion: a
// released_by value does not reproduce the string passed to --release — and
// neither does ANY record the settlement pushed to origin.
func TestAReleaseNeverRecordsTheOperatorIdentity(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The supervisor dies without settling anything: `lost`, held for a person.
	f.stopEverything()

	_, held, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", held.Failure)
	}

	// An identity in the operator's own words — exactly what --release takes,
	// and exactly what a public repository forbids in a tracked file.
	const who = "Peter Engelbrecht <peter.engelbrecht@example.com>"

	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, who)
	if err != nil {
		t.Fatalf("settle a1's lost attempt: %v", err)
	}
	if !settled.Recorded || settled.State != subprocess.StateLost {
		t.Fatalf("the settlement is %+v", settled)
	}
	// The OPERATOR-FACING answer keeps the real value: it goes to the
	// terminal, and the terminal is not the repository.
	if settled.ReleasedBy != who {
		t.Errorf("the settlement's answer to the operator is %q, want the value they passed", settled.ReleasedBy)
	}

	// The COMMITTED decision names a PERSON — a stable pseudonymous handle,
	// never the identity the person handed over.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var handle string
	for _, decision := range decisions {
		if decision.Request["op"] != settleOp || decision.Request["tick_id"] != "a1" {
			continue
		}
		by, _ := decision.Response["released_by"].(string)
		if by == "" {
			t.Fatalf("the release records no released_by: %+v", decision.Response)
		}
		if strings.Contains(who, by) || strings.Contains(by, "Peter") ||
			strings.Contains(by, "engelbrecht") || strings.Contains(by, "example.com") {
			t.Errorf("released_by reproduces the operator's identity: %q", by)
		}
		if by != releaseHandle(who) {
			t.Errorf("released_by is %q, want the stable handle releaseHandle(%q) = %q",
				by, who, releaseHandle(who))
		}
		handle = by
	}
	if handle == "" {
		t.Fatalf("no settlement decision on origin: %+v", decisions)
	}

	// No record the settlement pushed to origin carries the identity — not
	// the decision alone, but every .ticfac file at the pushed head, read as
	// RAW BYTES the way the repo's own guard would read them.
	head := originHeadOf(t, f, settler.IntegrationBranch())
	tree := mustRun(t, f.Repo.Origin, "git", "ls-tree", "-r", "--name-only", head)
	scanned := 0
	for _, path := range strings.Split(strings.TrimSpace(tree), "\n") {
		if path == "" || !strings.HasPrefix(path, runstate.Root+"/") {
			continue
		}
		raw := mustRun(t, f.Repo.Origin, "git", "show", head+":"+path)
		scanned++
		for _, needle := range []string{who, "peter.engelbrecht@example.com", "Peter Engelbrecht"} {
			if strings.Contains(raw, needle) {
				t.Errorf("%s at %s carries the operator identity (%q)", path, short(head), needle)
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("the pushed head carries no run-state records to scan: %s", head)
	}

	// The real value survives, in run-local state OUTSIDE the repository:
	// a person who can read the host can still join the handle to a name.
	local := settler.releaseRecordPath("a1", 1)
	if inside, err := filepath.Rel(f.Repo.Dir, local); err == nil && !strings.HasPrefix(inside, "..") {
		t.Errorf("the release identity is kept at %s, INSIDE the repository at %s", local, f.Repo.Dir)
	}
	raw, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("the release identity was not kept outside the repository: %v", err)
	}
	var kept releaseRecord
	if err := json.Unmarshal(raw, &kept); err != nil {
		t.Fatalf("the run-local release record is not readable: %v\n%s", err, raw)
	}
	if kept.ReleasedBy != who {
		t.Errorf("the run-local release record carries %q, want the real identity %q", kept.ReleasedBy, who)
	}
	if kept.Handle != handle {
		t.Errorf("the run-local release record names handle %q, want %q", kept.Handle, handle)
	}

	// The handle is a pure function of the input: the same operator reads as
	// the same person across releases, and a different operator does not.
	if releaseHandle(who) != releaseHandle(who) {
		t.Errorf("releaseHandle is not stable")
	}
	if releaseHandle(who) == releaseHandle("an operator") {
		t.Errorf("releaseHandle maps two different people to one handle")
	}

	// The next run still dispatches a new attempt: pseudonymity changed what
	// the record SAYS, never what the release DOES.
	f.Runner = fakeRunnerArgv(t, "report")
	next, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the release did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the run after the release ended %s: %s", resumed.State, resumed.Reason)
	}
	if got := next.Stages("a1"); !contains(got, StageSettled) || !contains(got, StageDispatched) {
		t.Fatalf("a1's stages %v do not show the released attempt skipped and a new one dispatched", got)
	}
}

// TestDigestOfIsAFullSha256 guards the other half: a digest labelled sha256
// IS one, whole. The truncation to 32 hex characters was a false label over
// 128 bits, and 32 hex is exactly the shape of a cloud account id, so every
// run record carrying one tripped a public-repo account detector.
func TestDigestOfIsAFullSha256(t *testing.T) {
	t.Parallel()
	got := digestOf("anything")
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("digestOf = %q, want the sha256: label", got)
	}
	body := strings.TrimPrefix(got, "sha256:")
	if len(body) != 64 {
		t.Errorf("digestOf = %q: %d hex characters after the label, want a FULL 64", got, len(body))
	}
	if _, err := hex.DecodeString(body); err != nil {
		t.Errorf("digestOf = %q: the body is not hex: %v", got, err)
	}
	// The length prefix still tells two different splits apart at full length.
	if digestOf("ab", "c") == digestOf("a", "bc") {
		t.Errorf("two different splits digest the same")
	}
	// Every digest field that lands in a committed record routes through
	// digestOf, so each of them is full-length by construction. The gate
	// digest is the one that reaches every evidence record as
	// context_manifest_digest; promptDigest reaches every attempt marker.
	if gate := (GateCommands{{Name: "tree", Command: "make test"}}).Digest(); len(gate) != len("sha256:")+64 {
		t.Errorf("a gate digest is %q, want sha256: and a full 64-hex digest", gate)
	}
	if prompt := promptDigest(&profile.Profile{Role: "implement-tick", Prompt: "do the work"}); len(prompt) != len("sha256:")+64 {
		t.Errorf("a prompt digest is %q, want sha256: and a full 64-hex digest", prompt)
	}
}
