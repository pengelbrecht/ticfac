package subprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// This package holds COPIES of two things the bundle defines: the status-line
// pattern with its decoration set, and the boundary's protected prefixes. An
// executor cannot read the bundle off disk at run time — it is a binary that
// runs wherever it was installed — so the copies are unavoidable.
//
// What is avoidable is the copies drifting, which is the failure
// collect-vocabulary.json was written about: re-spell a verdict on one side
// and two runs disagree about what happened to the same tick with nothing
// failing anywhere. So each copy is asserted against the fixture here, and the
// fixture's own cases are run against this implementation.

func readBundle(t *testing.T, name string, value any) {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

type collectVocabularyFixture struct {
	Verdicts struct {
		ReadyToMerge      string `json:"ready_to_merge"`
		NoCommits         string `json:"no_commits"`
		MissingResult     string `json:"missing_result"`
		BoundaryViolation string `json:"boundary_violation"`
	} `json:"verdicts"`
	Statuses struct {
		Done             string `json:"done"`
		DoneWithConcerns string `json:"done_with_concerns"`
		NeedsContext     string `json:"needs_context"`
		Blocked          string `json:"blocked"`
	} `json:"statuses"`
	NeedsHumanStatuses struct {
		Statuses []string `json:"statuses"`
	} `json:"needs_human_statuses"`
	StatusLinePattern struct {
		Pattern string `json:"pattern"`
	} `json:"status_line_pattern"`
	Decoration struct {
		Trimmed    string `json:"trimmed"`
		NotTrimmed string `json:"not_trimmed"`
	} `json:"decoration"`
	ParseCases struct {
		Cases []struct {
			Name   string `json:"name"`
			Body   string `json:"body"`
			Status string `json:"status"`
			Detail string `json:"detail"`
			Line   string `json:"line"`
		} `json:"cases"`
	} `json:"parse_cases"`
}

// The pattern and the trim set, byte for byte. The pattern is pinned as SOURCE
// upstream precisely so that a re-ordered alternation fails a build before it
// can invert a verdict at run time — a copy that is merely "equivalent" is not
// what the fixture pins.
func TestTheStatusPatternAndTrimSetAreTheBundles(t *testing.T) {
	var v collectVocabularyFixture
	readBundle(t, "collect-vocabulary.json", &v)

	if statusLinePattern != v.StatusLinePattern.Pattern {
		t.Errorf("this package's status-line pattern has drifted from the bundle:\n  here   %s\n  bundle %s",
			statusLinePattern, v.StatusLinePattern.Pattern)
	}
	if decorationCutset != v.Decoration.Trimmed {
		t.Errorf("this package trims %q and the bundle trims %q", decorationCutset, v.Decoration.Trimmed)
	}
	for _, r := range v.Decoration.NotTrimmed {
		if strings.ContainsRune(decorationCutset, r) {
			t.Errorf("%q is trimmed here and the bundle says it must not be", string(r))
		}
	}
}

// The vocabulary, spelled once. ticfac reports a tick's outcome in these
// words, and a fifth spelling here is a word the other three implementations
// do not know.
func TestTheVerdictAndStatusWordsAreTheBundles(t *testing.T) {
	var v collectVocabularyFixture
	readBundle(t, "collect-vocabulary.json", &v)

	for _, pair := range []struct{ here, bundle, what string }{
		{VerdictReadyToMerge, v.Verdicts.ReadyToMerge, "ready-to-merge"},
		{VerdictNoCommits, v.Verdicts.NoCommits, "no-commits"},
		{VerdictMissingResult, v.Verdicts.MissingResult, "missing-result"},
		{VerdictBoundaryViolation, v.Verdicts.BoundaryViolation, "boundary-violation"},
		{StatusDone, v.Statuses.Done, "DONE"},
		{StatusDoneWithConcerns, v.Statuses.DoneWithConcerns, "DONE_WITH_CONCERNS"},
		{StatusNeedsContext, v.Statuses.NeedsContext, "NEEDS_CONTEXT"},
		{StatusBlocked, v.Statuses.Blocked, "BLOCKED"},
	} {
		if pair.here != pair.bundle {
			t.Errorf("%s is %q here and %q in the bundle", pair.what, pair.here, pair.bundle)
		}
	}

	// The escalation set: dropping one silently stops a human being told.
	for _, status := range v.NeedsHumanStatuses.Statuses {
		if !ParseReport("STATUS: " + status + "\n").NeedsHuman() {
			t.Errorf("%s needs a human and this package does not flag it", status)
		}
	}
	for _, status := range []string{StatusDone, StatusDoneWithConcerns} {
		if ParseReport("STATUS: " + status + "\n").NeedsHuman() {
			t.Errorf("%s is not an escalation and this package flags it as one", status)
		}
	}
}

// The bundle's sixteen INPUT -> PARSED cases, run against this parser. They
// include the case the whole fixture exists for: a DONE_WITH_CONCERNS that a
// weakened pattern would read as its opposite.
func TestEveryParseCaseFromTheBundleParsesTheSameWayHere(t *testing.T) {
	var v collectVocabularyFixture
	readBundle(t, "collect-vocabulary.json", &v)

	if len(v.ParseCases.Cases) == 0 {
		t.Fatal("the bundle carries no parse cases")
	}
	for _, c := range v.ParseCases.Cases {
		report := ParseReport(c.Body)
		if report.Status != c.Status {
			t.Errorf("%s: status %q, contract says %q", c.Name, report.Status, c.Status)
		}
		if report.Detail != c.Detail {
			t.Errorf("%s: detail %q, contract says %q", c.Name, report.Detail, c.Detail)
		}
		if report.Line != c.Line {
			t.Errorf("%s: line %q, contract says %q", c.Name, report.Line, c.Line)
		}
	}
}

// The review-verdict line (tick b50): the review-epic role's own judgement,
// parsed with the same discipline the status line is — the FINAL line wins,
// markdown decoration is trimmed, and the alternation order plus the \b guard
// stop a suffixed word reading as a bare one. The vocabulary is ticfac's own
// rather than the bundle's (see report.go for why), so the cases that pin it
// live here.
func TestTheReviewVerdictLineParses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		verdict string
		detail  string
	}{{
		name:    "a plain READY",
		body:    "REVIEW-VERDICT: READY\n",
		verdict: ReviewVerdictReady,
	}, {
		name:    "a plain NOT READY",
		body:    "REVIEW-VERDICT: NOT READY\n",
		verdict: ReviewVerdictNotReady,
	}, {
		name:    "not ready with what would make it ready",
		body:    "REVIEW-VERDICT: NOT READY — the gate run never happened\n",
		verdict: ReviewVerdictNotReady,
		detail:  "the gate run never happened",
	}, {
		name:    "a colon separator",
		body:    "REVIEW-VERDICT: READY: all waves integrate and the gate ran\n",
		verdict: ReviewVerdictReady,
		detail:  "all waves integrate and the gate ran",
	}, {
		name: "the FINAL verdict line wins",
		body: "REVIEW-VERDICT: READY\n" +
			"\nThen I looked again.\n\n" +
			"REVIEW-VERDICT: NOT READY — the last word is the one that counts\n",
		verdict: ReviewVerdictNotReady,
		detail:  "the last word is the one that counts",
	}, {
		name:    "inside a bolded bullet",
		body:    "Some prose first.\n\n- **REVIEW-VERDICT: NOT READY — see defect 3**\n",
		verdict: ReviewVerdictNotReady,
		detail:  "see defect 3",
	}, {
		name: "tab separator with a hyphen",
		body: "REVIEW-VERDICT:\tNOT READY\t- the reconciler never ran\n",
		verdict: ReviewVerdictNotReady,
		detail:  "the reconciler never ran",
	}, {
		name:    "a word with a suffix is not a verdict",
		body:    "REVIEW-VERDICT: READINESS\n",
		verdict: "",
	}, {
		name:    "a judgement word outside the vocabulary",
		body:    "REVIEW-VERDICT: MAYBE\n",
		verdict: "",
	}, {
		name:    "the vocabulary is uppercase as the review writes it",
		body:    "REVIEW-VERDICT: not ready — said in prose casing\n",
		verdict: "",
	}, {
		name:    "no verdict line at all",
		body:    "STATUS: DONE — the review's prose says it is not ready\n",
		verdict: "",
	}}
	for _, c := range cases {
		report := ParseReport(c.body)
		if report.ReviewVerdict != c.verdict {
			t.Errorf("%s: verdict %q, want %q", c.name, report.ReviewVerdict, c.verdict)
		}
		if report.ReviewVerdictDetail != c.detail {
			t.Errorf("%s: detail %q, want %q", c.name, report.ReviewVerdictDetail, c.detail)
		}
	}
}

// The verdict line and the status line are TWO lines, and neither stands in
// for the other: a report whose verdict rides only in the status detail is a
// report whose judgement is prose, which is the gap this tick exists to close.
func TestTheStatusLineIsNotAReviewVerdictAndViceVersa(t *testing.T) {
	t.Parallel()
	report := ParseReport("REVIEW-VERDICT: NOT READY — the gate run never happened\n\nSTATUS: DONE_WITH_CONCERNS — see above\n")
	if report.Status != StatusDoneWithConcerns {
		t.Errorf("status %q, want DONE_WITH_CONCERNS: the verdict line must not displace the status", report.Status)
	}
	if report.ReviewVerdict != ReviewVerdictNotReady {
		t.Errorf("review verdict %q, want NOT READY: the status line must not displace the verdict", report.ReviewVerdict)
	}
	// A report that ends READY in its own vocabulary and DONE in the status
	// vocabulary states two facts about two questions, and both parse.
	both := ParseReport("REVIEW-VERDICT: READY\n\nSTATUS: DONE\n")
	if both.Status != StatusDone || both.ReviewVerdict != ReviewVerdictReady {
		t.Errorf("status %q verdict %q: the two lines are independent answers", both.Status, both.ReviewVerdict)
	}
	if both.NeedsHuman() {
		t.Error("a READY verdict is not an escalation")
	}
}

type lifecycleFixture struct {
	Harness struct {
		ProtectedPrefixes struct {
			Prefixes []string `json:"prefixes"`
		} `json:"protected_prefixes"`
		Guards []struct {
			Guard string `json:"guard"`
		} `json:"guards"`
	} `json:"harness"`
	Invariants []struct {
		ID     string   `json:"id"`
		Title  string   `json:"title"`
		Guards []string `json:"guards"`
	} `json:"invariants"`
}

// A10's boundary lives in the fixture, not in this reader. The prefixes here
// are read from it rather than described by it, so the repository's real
// boundary and the one this executor enforces cannot drift apart with both
// suites green.
func TestTheProtectedPrefixesAreTheFixtures(t *testing.T) {
	var c lifecycleFixture
	readBundle(t, "lifecycle-invariants.json", &c)

	want := c.Harness.ProtectedPrefixes.Prefixes
	if len(want) == 0 {
		t.Fatal("the fixture declares no protected prefixes")
	}
	if strings.Join(protectedPrefixes, "|") != strings.Join(want, "|") {
		t.Fatalf("this executor protects %v and the fixture protects %v", protectedPrefixes, want)
	}
	for _, prefix := range want {
		path := prefix + "records/x.json"
		if !OutsideBoundary(path) {
			t.Errorf("%s is under a protected prefix and this executor permits it", path)
		}
	}
	if OutsideBoundary("internal/exec/subprocess/executor.go") {
		t.Error("an ordinary source path is being refused as a boundary violation")
	}
}

// The exemptions are the narrow half of the rule, and they are narrow on
// purpose: a worker may amend the run's configuration, the runner table and
// the learnings a retro compacts, and may never write a tracker RECORD.
func TestTheExemptionsAreNarrowAndTheRecordsAreNot(t *testing.T) {
	for _, exempt := range exemptFromBoundary {
		under := false
		for _, prefix := range protectedPrefixes {
			under = under || strings.HasPrefix(exempt, prefix)
		}
		if !under {
			t.Errorf("%s is exempted from a boundary it is not under; the exemption does nothing", exempt)
		}
		if OutsideBoundary(exempt) {
			t.Errorf("%s is exempt and is being refused", exempt)
		}
	}
	for _, record := range []string{".tick/issues/abc.json", ".tick/activity/2026-09-02.jsonl", ".ticfac/runs/run-1/state.json"} {
		if !OutsideBoundary(record) {
			t.Errorf("%s is a record under another authority and this executor permits it", record)
		}
	}
	// A file whose name merely starts like a protected directory is not under
	// it: `.tickle` is not `.tick/`.
	if OutsideBoundary(".tickle/notes.md") {
		t.Error(".tickle/notes.md is being refused as though it were under .tick/")
	}
}

// Two failures never share one sentence. The class field has six values for
// many more failures, so the message is what a person actually reads, and "the
// run broke" is the message that sends a diagnosis at the wrong problem.
func TestNoTwoVerdictsShareAFailureMessage(t *testing.T) {
	seen := map[string]string{}
	for verdict, message := range failureMessages {
		if message == "" {
			continue
		}
		if other, ok := seen[message]; ok {
			t.Errorf("%s and %s report the same sentence: %q", verdict, other, message)
		}
		seen[message] = verdict
	}
	if len(seen) < 3 {
		t.Errorf("only %d failing verdicts carry a message of their own", len(seen))
	}
}
