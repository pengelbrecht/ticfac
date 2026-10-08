package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The classification decision record, PINNED byte for byte as this package
// writes it (tick kl9).
//
// The bundle leaves the decision record's own `role` a free string for each
// reader to close, and the two run-state stores close it independently: this
// side's DecisionRoles carries the classification exchange, and the
// TypeScript store had closed it with $defs.role alone — a vocabulary a
// classify-tick record can never satisfy, so any run that classified left
// a decision record that made store.decisions() THROW on the Worker. A
// vocabulary fixed on one side only is a record the other side cannot read,
// and neither suite could see it: each tested against its own fixtures.
//
// So the record is pinned exactly as the real exchange writes it — the real
// reconciler over the real store, on a real repository — at
// cloudflare/test/fixtures/classify-tick-decision.json. The fixture kept
// the location it was born in (tick kl9) even though the TypeScript store
// that was its second reader went with the Workflow reconciler (tick mn7
// deleted run-state-store.ts and its suite): internal/runstate's reader test
// still decodes and validates it through the same decoder this store reads
// a run with, so the pin cannot drift into a record the Go reader refuses.
//
// Exactly one value in the record cannot be byte-stable across runs: the
// provenance's source_sha, a fresh commit every fixture mints. It is
// canonicalized — with the replaced value's SHAPE asserted first, so a writer
// that stops putting a commit sha there still fails the regeneration instead
// of silently pinning a placeholder (the tk-write-corpus rule).
//
// Regenerate deliberately, never hand-edit:
//
//	go test ./internal/reconcile -run TestTheClassificationRecordIsPinnedAsItIsWritten -update-pin
const classificationPin = "../../cloudflare/test/fixtures/classify-tick-decision.json"

var updatePin = flag.Bool("update-pin", false, "rewrite the pinned classification decision record from the live exchange")

// sha40 is what a commit sha is here: 40 lowercase hex characters. The
// canonicalization asserts the shape of everything it replaces.
var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// The pinned record's clock: the one fixed instant both `requested_at` and
// `answered_at` carry, so the pin reads as one exchange that happened once.
var classificationPinInstant = time.Date(2026, 9, 23, 9, 41, 0, 0, time.UTC)

func TestTheClassificationRecordIsPinnedAsItIsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Now = func() time.Time { return classificationPinInstant }
	opts.Classifier = &countingClassifier{answer: classificationAnswer}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)
	entry := planEntry{TickID: "a1", Role: "implement-tick"}
	if _, err := r.classificationFor(context.Background(), entry, true); err != nil {
		t.Fatalf("the exchange refused a role-less tick: %v", err)
	}

	// The bytes exactly as the run branch holds them — read from the ref, not
	// re-encoded through this test's own types, because a fixture written
	// from the same reading as the code is what the pin exists not to be.
	path := ".ticfac/runs/" + r.runID + "/decisions/1.json"
	raw := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+r.branch+":"+path)

	// Canonicalize the one value a fresh fixture always mints fresh. The
	// shape is asserted BEFORE the substitution, so the placeholder can
	// never hide a change of shape.
	var probe struct {
		Provenance struct {
			SourceSHA string `json:"source_sha"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("the record on the run branch does not decode: %v", err)
	}
	if !sha40.MatchString(probe.Provenance.SourceSHA) {
		t.Fatalf("the record's provenance.source_sha is %q, which is not a commit sha; the canonicalization would hide a shape change", probe.Provenance.SourceSHA)
	}
	pinned := strings.Replace(raw, probe.Provenance.SourceSHA, "0000000000000000000000000000000000000000", 1)

	if *updatePin {
		if err := os.WriteFile(classificationPin, []byte(pinned), 0o644); err != nil {
			t.Fatalf("rewrite the pin: %v", err)
		}
		return
	}
	want, err := os.ReadFile(classificationPin)
	if err != nil {
		t.Fatalf("the pin is missing — regenerate it deliberately with -update-pin: %v", err)
	}
	if !bytes.Equal([]byte(pinned), want) {
		t.Fatalf("the classification record this package writes is not what the pin holds.\n"+
			"Adopt the change DELIBERATELY: go test ./internal/reconcile -run TestTheClassificationRecordIsPinnedAsItIsWritten -update-pin,\n"+
			"then read the diff — both suites test against that pin.\nwritten:\n%s\npinned:\n%s", pinned, want)
	}
}

// The pin's profile_digest has a SHORT side to its comparison (tick 3sd).
//
// The writer above was, until this test, the only comparison between the pin
// and the profile set its run resolved — and it is EndToEnd, so the per-tick
// gate skipped it. Tick 7sn edited profiles/closeout-epic.md, which moves
// profileSetDigest and therefore the profile_digest of every classification
// record the pin stands for; `make gate` stayed green at the tick that made
// the edit and only the epic branch's CI went red on it, hours later (tick
// 4w7 regenerated the pin; this guard is the recurrence half).
//
// So the digest is re-derived SHORT and held against the pin. It is derived
// the way the writer's run derives it, minus the repository: the embedded
// profile set, routed through the fixture's own gate cell — which mirrors the
// shipped implement-tick profile, so it changes no resolved value — with no
// tier and no named config. A profile edit now fails the gate at the tick
// that made it, naming the pin to regenerate, instead of failing CI on an
// epic branch nobody is watching.
//
// The substrate is resolved BLIND here rather than the way New resolves it.
// The fixture's repository declares no runners.local.toml or runners.cloud.toml,
// so no substrate the decision procedure can answer changes a routed value and
// the blind resolution digests the same set; what a real repository's
// resolution adds — an overlay file, a selected config, a tier — is the
// EndToEnd writer's half, in CI and at close-out.
//
// short: resolves the embedded profiles over one temp config and reads the pin; no repository is built
func TestTheClassificationPinsProfileDigestIsTheEmbeddedProfileSet(t *testing.T) {
	t.Parallel()

	// The pin, read through the same closed record type the store reads it
	// with, so a pin that stops being a decision is refused here rather than
	// quietly compared on one field.
	raw, err := os.ReadFile(classificationPin)
	if err != nil {
		t.Fatalf("the pin is missing — regenerate it deliberately with -update-pin: %v", err)
	}
	var pinned runstate.Decision
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatalf("the pin does not read back as a decision record: %v", err)
	}
	if pinned.Provenance.ProfileDigest == nil {
		t.Fatal("the pin states no profile_digest, so nothing ties the record it pins to a profile set — " +
			"regenerate it deliberately with -update-pin")
	}

	// The profile set, resolved as the writer's run resolves it over the
	// fixture's own gate: the embedded profiles, the [roles.implement] cell
	// the fixture declares, no tier, no named config.
	gate := filepath.Join(t.TempDir(), "runners.toml")
	if err := os.WriteFile(gate, []byte(passingGate), 0o644); err != nil {
		t.Fatal(err)
	}
	profiles, err := profile.ResolveAll(profile.Options{RunnersConfig: gate})
	if err != nil {
		t.Fatalf("the embedded profiles no longer resolve under the fixture's own gate: %v", err)
	}
	if got := profileSetDigest(profiles); got != *pinned.Provenance.ProfileDigest {
		t.Fatalf("the pin's profile_digest is %q, but the embedded profile set it was pinned under digests to %q.\n"+
			"Adopt the change DELIBERATELY: go test ./internal/reconcile -run TestTheClassificationRecordIsPinnedAsItIsWritten -update-pin,\n"+
			"then read the diff — this test, the EndToEnd writer above and internal/runstate's reader all test against that pin.\n"+
			"(This is the short half of the comparison, so the per-tick gate pays for it at the tick that edits a profile\n"+
			"rather than an epic branch's CI paying for it hours later.)",
			*pinned.Provenance.ProfileDigest, got)
	}
}
