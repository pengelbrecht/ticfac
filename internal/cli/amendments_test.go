package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The amendments surface (tick 7sn): the operator's half of the channel a
// worker's tracker-edit proposal is the other half of. What has to hold:
//
//   - `amendments` lists the worker-proposed notes on the epic's record with
//     their keys and states, and says how to settle each — each addressed by
//     the shortest key prefix that names it alone, the same friction rule the
//     triage listing teaches;
//   - `amendment` records ONE attributed decision, confirm or reject, and a
//     decision is never made twice — the second call is not an error, it
//     reports the standing decision;
//   - the listing and the decision read and write the same durable record the
//     close-out's amendments gate holds the hand-over on.

func seedAmendment(t *testing.T, repo string, amendment runstate.Amendment) {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutAmendment(amendment); err != nil {
		t.Fatal(err)
	}
}

func testAmendmentRecord(key string) runstate.Amendment {
	p := runstate.Provenance{
		RunID:          "epic-qeu",
		TickID:         runstate.Ptr("b1"),
		Attempt:        runstate.Ptr(1),
		SourceRef:      "refs/heads/epic/qeu",
		SourceSHA:      "acb08b9493dd8647918efbebac27079c64339946",
		IntegrationRef: runstate.Ptr("refs/heads/epic/qeu"),
		Phase:          runstate.PhaseWorker,
		Executor:       runstate.Ptr("local-subprocess"),
		Role:           runstate.Ptr("implement-tick"),
	}
	return runstate.Amendment{
		Key:        key,
		Source:     "ticfac-worker",
		EpicID:     "qeu",
		Field:      runstate.AmendmentFieldNotes,
		Value:      "tick b1: the slow-check gate is excepted from A1 on the record, now on the record",
		ProposedBy: "b1",
		Attempt:    1,
		ProposedAt: "2026-10-05T18:25:00Z",
		Status:     runstate.AmendmentPending,
		Provenance: p,
	}
}

func TestAmendmentsListsTheNotesAndHowToSettleThem(t *testing.T) {
	repo := newFindingsRepo(t)
	seedAmendment(t, repo, testAmendmentRecord("5abf2252"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"amendments", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"5abf2252", "pending", "notes", "qeu", "b1",
		"tick b1: the slow-check gate is excepted from A1 on the record, now on the record",
		"ticfac amendment qeu 5ab --confirm --by \"<who>\"",
		"the epic's close-out does not hand over while one is undecided",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}
	// The full 64-hex key is the address of record, never the command a person
	// is taught to type: the pointer names the short prefix, the same rule the
	// findings listing keeps.
	if strings.Contains(out, "ticfac amendment qeu 5abf2252") {
		t.Errorf("stdout still teaches the 64-hex key:\n%s", out)
	}
}

// The prefix the pointer shows names ONE amendment: two amendments sharing a
// prefix get a longer one, because a person who copies the pointer's prefix
// into `ticfac amendment` must land on the note they read.
func TestTheAmendmentsPointerNamesOneAmendment(t *testing.T) {
	repo := newFindingsRepo(t)
	seedAmendment(t, repo, testAmendmentRecord("5abf2252"))
	other := testAmendmentRecord("5abf0718")
	other.Value = "tick c3: a different note on the epic"
	seedAmendment(t, repo, other)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"amendments", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ticfac amendment qeu 5abf2 --confirm") ||
		!strings.Contains(out, "ticfac amendment qeu 5abf0 --confirm") {
		t.Errorf("the pointers do not disambiguate the two amendments:\n%s", out)
	}
}

// THE DECISION: attributed, one of two verdicts, and never made twice — the
// second call reports the standing decision rather than erroring or redoing it.
func TestAmendmentRecordsTheOperatorsDecision(t *testing.T) {
	repo := newFindingsRepo(t)
	seedAmendment(t, repo, testAmendmentRecord("5abf2252"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"amendment", "--repo", repo, "qeu", "5ab", "--confirm", "--by", "the operator"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("confirm: exit %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "CONFIRMED") || !strings.Contains(out, "the operator") {
		t.Errorf("the confirmation does not read as the operator's own:\n%s", out)
	}

	// The decision is on the record the close-out's gate reads, durably.
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	amendment, ok, err := store.Amendment("5abf2252")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if amendment.Status != runstate.AmendmentConfirmed || amendment.DecidedBy != "the operator" {
		t.Errorf("the decision did not land on the record: %+v", amendment)
	}

	// The second decision is not an error and changes nothing.
	stdout.Reset()
	if code := Run([]string{"amendment", "--repo", repo, "qeu", "5ab", "--reject", "--by", "someone else"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("second decision: exit %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "already confirmed") {
		t.Errorf("the second decision does not report the standing one:\n%s", out)
	}
	if amendment, _, err := store.Amendment("5abf2252"); err != nil || amendment.Status != runstate.AmendmentConfirmed {
		t.Errorf("a second decision moved a confirmed amendment: %+v (%v)", amendment, err)
	}
}

// An unattributed decision is refused — a decision nobody can attribute is one
// nobody can audit — and so is a decision that is neither verdict, and an
// address that names no amendment is a refusal that teaches the listing.
func TestAmendmentRefusesWhatItCannotAttributeOrAddress(t *testing.T) {
	repo := newFindingsRepo(t)
	seedAmendment(t, repo, testAmendmentRecord("5abf2252"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"amendment", "--repo", repo, "qeu", "5ab", "--confirm"}, &stdout, &stderr); code == 0 {
		t.Error("an unattributed decision was accepted")
	} else if !strings.Contains(stderr.String(), "--by names who is deciding") {
		t.Errorf("the refusal does not teach --by: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"amendment", "--repo", repo, "qeu", "5ab", "--confirm", "--reject", "--by", "x"},
		&stdout, &stderr); code == 0 {
		t.Error("a decision carrying both verdicts was accepted")
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"amendment", "--repo", repo, "qeu", "c0ffee", "--confirm", "--by", "the operator"},
		&stdout, &stderr); code == 0 {
		t.Error("an address naming no amendment was accepted")
	} else if !strings.Contains(stderr.String(), "ticfac amendments qeu") {
		t.Errorf("the refusal does not teach the listing: %s", stderr.String())
	}
}

// The empty listing states the absence rather than nothing — a command that
// prints nothing reads as "the surface is broken", not "there are none".
func TestAmendmentsStatesTheAbsence(t *testing.T) {
	repo := newFindingsRepo(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"amendments", "--repo", repo, "qeu"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "no worker-proposed amendments") {
		t.Errorf("the empty listing does not state the absence:\n%s", out)
	}
}
