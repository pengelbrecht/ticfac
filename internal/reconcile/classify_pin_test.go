package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
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
