package parity

import (
	"regexp"
	"strings"
	"testing"
)

// contracts/collect-vocabulary.json — EXECUTABLE.
//
// The verdict and status vocabulary a collect result is spelled in, plus the
// status-line regexp and sixteen INPUT -> PARSED cases. It is the bundle's
// three-sided fixture: ticks' herd collect, ticks' cloud collect and the
// factory Worker all read it, and ticfac is the fourth — the reconciler
// reports a tick's outcome in exactly these words, and a verdict re-spelled on
// one side makes two runs disagree about what happened with nothing failing.
//
// Everything here is executable from the fixture: the pattern is pinned as
// regexp source, and Go's regexp accepts the same syntax JavaScript's does for
// the constructs it uses. That is the whole point of pinning the SOURCE rather
// than a description of it.

const collectVocabularyFile = "collect-vocabulary.json"

type collectVocabulary struct {
	Verdicts struct {
		ReadyToMerge      string `json:"ready_to_merge"`
		NoCommits         string `json:"no_commits"`
		MissingResult     string `json:"missing_result"`
		BoundaryViolation string `json:"boundary_violation"`
	} `json:"verdicts"`
	RemoteOnlyVerdicts struct {
		Unknown string `json:"unknown"`
	} `json:"remote_only_verdicts"`
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

// parseStatus is ticfac's implementation of the rule: the FINAL status line of
// a report, split into the status word, the detail after it, and the raw line.
// Everything is empty when the report carries no recognisable status — which
// is the `missing-result` verdict, so a case that stops matching here is a
// verdict change too.
func parseStatus(body, pattern, decoration string) (status, detail, line string) {
	statusLine := regexp.MustCompile(pattern)
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.Trim(strings.TrimRight(raw, "\r"), decoration)
		m := statusLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		// Keep scanning: the contract is the *final* status line, and a report
		// may quote the template's four options above its own answer.
		status = m[1]
		detail = strings.TrimSpace(strings.Trim(m[2], decoration))
		line = trimmed
	}
	return status, detail, line
}

func TestCollectVocabularyParsesEveryCase(t *testing.T) {
	var v collectVocabulary
	readContract(t, collectVocabularyFile, &v)

	if v.StatusLinePattern.Pattern == "" {
		t.Fatal("the contract pins no status-line pattern")
	}
	if _, err := regexp.Compile(v.StatusLinePattern.Pattern); err != nil {
		t.Fatalf("the pinned pattern does not compile in Go: %v", err)
	}
	if len(v.ParseCases.Cases) == 0 {
		t.Fatal("the contract carries no parse cases")
	}

	for _, c := range v.ParseCases.Cases {
		status, detail, line := parseStatus(c.Body, v.StatusLinePattern.Pattern, v.Decoration.Trimmed)
		if status != c.Status {
			t.Errorf("%s: status %q, contract says %q", c.Name, status, c.Status)
		}
		if detail != c.Detail {
			t.Errorf("%s: detail %q, contract says %q", c.Name, detail, c.Detail)
		}
		if line != c.Line {
			t.Errorf("%s: line %q, contract says %q", c.Name, line, c.Line)
		}
	}
}

// The alternation order is the rule the fixture's most valuable case exists
// for: DONE_WITH_CONCERNS before DONE, so a weakened pattern cannot read the
// escalation as its opposite. Asserted directly, not only through the cases.
func TestDoneWithConcernsPrecedesDoneInTheAlternation(t *testing.T) {
	var v collectVocabulary
	readContract(t, collectVocabularyFile, &v)

	withConcerns := strings.Index(v.StatusLinePattern.Pattern, v.Statuses.DoneWithConcerns)
	done := strings.Index(v.StatusLinePattern.Pattern, "|"+v.Statuses.Done+"|")
	if withConcerns < 0 || done < 0 {
		t.Fatalf("the pattern does not carry both status words: %s", v.StatusLinePattern.Pattern)
	}
	if withConcerns > done {
		t.Errorf("%s follows %s in the alternation; the escalation would be truncated to its opposite",
			v.Statuses.DoneWithConcerns, v.Statuses.Done)
	}
	if !strings.Contains(v.StatusLinePattern.Pattern, `\b`) {
		t.Error("the pattern has lost its \\b guard; DONEISH would parse as DONE")
	}
}

// The decoration set is pinned BEHAVIOURALLY, from both sides: every character
// in `trimmed` must be stripped and every character in `not_trimmed` must not
// be. A set that quietly grows fails as loudly as one that shrinks.
func TestDecorationSetIsPinnedFromBothSides(t *testing.T) {
	var v collectVocabulary
	readContract(t, collectVocabularyFile, &v)

	line := "STATUS: " + v.Statuses.Done
	for _, r := range v.Decoration.Trimmed {
		body := string(r) + line + string(r) + "\n"
		status, _, _ := parseStatus(body, v.StatusLinePattern.Pattern, v.Decoration.Trimmed)
		if status != v.Statuses.Done {
			t.Errorf("%q is in the trim set and was not stripped: %q parsed as %q", string(r), body, status)
		}
	}
	for _, r := range v.Decoration.NotTrimmed {
		body := string(r) + line + string(r) + "\n"
		status, _, _ := parseStatus(body, v.StatusLinePattern.Pattern, v.Decoration.Trimmed)
		if status != "" {
			t.Errorf("%q is NOT in the trim set and was stripped anyway: %q parsed as %q", string(r), body, status)
		}
	}
}

// The vocabularies are closed, and each verdict and status is spelled exactly
// once. ticfac reports in these words; a fifth verdict invented here would be
// a word the other two implementations do not know.
func TestCollectVocabularyIsClosedAndDistinct(t *testing.T) {
	var v collectVocabulary
	readContract(t, collectVocabularyFile, &v)

	verdicts := []string{
		v.Verdicts.ReadyToMerge, v.Verdicts.NoCommits,
		v.Verdicts.MissingResult, v.Verdicts.BoundaryViolation,
	}
	statuses := []string{
		v.Statuses.Done, v.Statuses.DoneWithConcerns,
		v.Statuses.NeedsContext, v.Statuses.Blocked,
	}
	seen := map[string]bool{}
	for _, word := range append(append([]string{}, verdicts...), statuses...) {
		if word == "" {
			t.Fatalf("a verdict or status is empty: verdicts=%v statuses=%v", verdicts, statuses)
		}
		if seen[word] {
			t.Errorf("%q appears twice in the vocabulary", word)
		}
		seen[word] = true
	}

	// `unknown` is the remote-only verdict: it means the durable evidence
	// could not be READ, and is never a verdict ON the worker. An
	// implementation that reads no remote must not define it, so it is
	// deliberately kept out of the four above.
	if v.RemoteOnlyVerdicts.Unknown == "" {
		t.Error("the contract names no remote-only verdict")
	}
	if seen[v.RemoteOnlyVerdicts.Unknown] {
		t.Errorf("%q is both a verdict and the remote-only verdict", v.RemoteOnlyVerdicts.Unknown)
	}

	// The escalation set is a subset of the four, and dropping one of them
	// silently stops a human being told.
	if len(v.NeedsHumanStatuses.Statuses) == 0 {
		t.Fatal("no status is an escalation, so nothing reaches a human")
	}
	known := map[string]bool{}
	for _, s := range statuses {
		known[s] = true
	}
	for _, s := range v.NeedsHumanStatuses.Statuses {
		if !known[s] {
			t.Errorf("%q needs a human and is not one of the four statuses", s)
		}
	}
}

// Every status word the parse cases produce is one of the four, and each of
// the four is reached. A vocabulary no case exercises is a word nothing has
// ever parsed.
func TestEveryStatusIsReachedByACase(t *testing.T) {
	var v collectVocabulary
	readContract(t, collectVocabularyFile, &v)

	reached := map[string]bool{}
	for _, c := range v.ParseCases.Cases {
		if c.Status != "" {
			reached[c.Status] = true
		}
	}
	for _, s := range []string{v.Statuses.Done, v.Statuses.DoneWithConcerns, v.Statuses.NeedsContext, v.Statuses.Blocked} {
		if !reached[s] {
			t.Errorf("no parse case produces %s", s)
		}
	}
	if !hasEmptyStatusCase(v) {
		t.Error("no parse case produces an empty status — the missing-result path is untested")
	}
}

func hasEmptyStatusCase(v collectVocabulary) bool {
	for _, c := range v.ParseCases.Cases {
		if c.Status == "" {
			return true
		}
	}
	return false
}
