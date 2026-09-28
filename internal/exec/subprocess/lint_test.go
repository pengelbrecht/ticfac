package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report checker (tick 4m6).

// THE PROPERTY: any report the checker accepts, collect accepts. Reports are
// generated from the pieces workers actually get wrong — the status line, the
// findings block in both formats with every field valid or not, the review's
// verdict line — and each is read by the checker and by collect's own reader.
//
// short: pure parsing over generated strings, no processes or repositories.
func TestAnyReportTheCheckerAcceptsCollectAccepts(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(4))
	roles := []string{"implement-tick", "review-epic", "closeout-epic", "resolve-conflict", ""}
	accepted, refused := 0, 0
	for i := 0; i < 20000; i++ {
		role := roles[rng.Intn(len(roles))]
		body := generateReport(rng)
		ctx := LintContext{Role: role}
		if rng.Intn(2) == 0 {
			ctx.AcceptanceItems = []string{"A1", "A2"}
			ctx.CommandIDs = []string{"go", "lint"}
		}
		lint := LintReport(body, ctx)
		refusal := ReportRefusal(role, ParseReport(body))
		if lint.Clean() {
			accepted++
			report := ParseReport(body)
			if len(refusal) > 0 || report.Status == "" || report.FindingsProblem != "" {
				t.Fatalf("the checker accepted a report collect refuses (%v, status %q, problem %q):\n%s",
					refusal, report.Status, report.FindingsProblem, body)
			}
		} else {
			refused++
		}
		// And every fatal problem collect would refuse over is one the
		// checker reports, so the worker is always told about it.
		for _, problem := range refusal {
			found := false
			for _, e := range lint.Errors {
				if e.Fatal && strings.HasPrefix(problem, e.Where+": ") {
					found = true
				}
			}
			if !found {
				t.Fatalf("collect refuses over %q, which the checker does not report:\n%s", problem, body)
			}
		}
	}
	if accepted < 500 || refused < 500 {
		t.Fatalf("the generator is lopsided: %d accepted, %d refused", accepted, refused)
	}
}

func generateReport(rng *rand.Rand) string {
	var b strings.Builder
	b.WriteString("# report\n\nSome prose about the work.\n\n")
	pick := func(options ...string) string { return options[rng.Intn(len(options))] }
	if rng.Intn(4) == 0 {
		b.WriteString(pick("STATUS: DONE (a quoted template)\n", "Status: done\n", "STATUS: COMPLETE\n"))
	}
	switch rng.Intn(6) {
	case 0:
		// no block
	case 1:
		b.WriteString(pick("```findings\n", "```findings v1\n", "```findings v2\n") + "[]\n```\n\n")
	case 2:
		b.WriteString("```findings\n[{\"kind\": \"defect\",\n```\n\n")
	case 3:
		b.WriteString(pick("```findings v2\n", "```findings\n"))
		b.WriteString(genItems(rng, true) + "\n```\n\n")
	default:
		b.WriteString(pick("```findings\n", "```findings v1\n"))
		b.WriteString(genItems(rng, false) + "\n```\n\n")
	}
	if rng.Intn(3) != 0 {
		b.WriteString(pick("REVIEW-VERDICT: READY\n", "REVIEW-VERDICT: NOT READY — the gate never ran\n",
			"review-verdict: ready\n", "REVIEW-VERDICT: MAYBE\n"))
	}
	switch rng.Intn(6) {
	case 0:
		// no status line
	case 1:
		b.WriteString("STATUS: FINISHED\n")
	default:
		b.WriteString(pick("STATUS: DONE\n", "STATUS: DONE_WITH_CONCERNS — check X\n", "STATUS: BLOCKED\n",
			"**STATUS: NEEDS_CONTEXT — which repo**\n"))
	}
	if rng.Intn(5) == 0 {
		b.WriteString("\nA line after the status.\n")
	}
	return b.String()
}

func genItems(rng *rand.Rand, v2 bool) string {
	n := rng.Intn(3) + 1
	items := make([]string, 0, n)
	pick := func(options ...string) string { return options[rng.Intn(len(options))] }
	for i := 0; i < n; i++ {
		fields := map[string]any{}
		maybe := func(key string, value any) {
			if rng.Intn(6) != 0 {
				fields[key] = value
			}
		}
		if v2 {
			maybe("kind", pick("defect", "proposal", "contract-change", "upstream-tick", "wish"))
			maybe("title", pick("a short title", "", strings.Repeat("long ", 20)))
			maybe("severity", pick("low", "medium", "high", "urgent"))
			if rng.Intn(2) == 0 {
				fields["target"] = pick("", "pengelbrecht/ticks", "the other repo", "pengelbrecht/ticks (bundle)")
			}
			if rng.Intn(2) == 0 {
				fields["breaks"] = map[string]any{"item": pick("A1", "A9", "none", "3"), "check": pick("go", "none", "go test ./...", "the tests")}
			}
			if rng.Intn(3) == 0 {
				fields["breaks"] = pick("A1", "none")
			}
			if rng.Intn(2) == 0 {
				fields["evidence"] = "x.go:1"
			}
		} else {
			maybe("kind", pick("defect", "proposed-tick", "upstream-tick", "contract", "wish"))
			maybe("title", pick("a short title", "", strings.Repeat("long ", 20)))
			maybe("body", "b")
			maybe("severity", pick("low", "high", "urgent"))
			maybe("target", pick("", "pengelbrecht/ticks", "elsewhere"))
			if rng.Intn(2) == 0 {
				fields["done_item"] = pick("A1", "none", "item 3")
			}
			if rng.Intn(2) == 0 {
				fields["demonstrating_check"] = pick("go", "none")
			}
		}
		if rng.Intn(5) == 0 {
			fields["title_note"] = "an annotation"
		}
		if rng.Intn(10) == 0 {
			fields["title"] = 42
		}
		raw, _ := json.Marshal(fields)
		items = append(items, string(raw))
	}
	return "[" + strings.Join(items, ",\n") + "]"
}

// The v2 shape, read onto the record every reader downstream already knows.
//
// short: pure parsing.
func TestAV2BlockMapsOntoTheRecord(t *testing.T) {
	t.Parallel()
	body := "```findings v2\n" + `[
  {"kind": "defect", "title": "a defect here", "severity": "high",
   "breaks": {"item": "A3", "check": "go"}, "evidence": "internal/x.go:42"},
  {"kind": "proposal", "title": "a tick for ticks", "severity": "low", "target": "pengelbrecht/ticks"},
  {"kind": "proposal", "title": "a tick for this repo", "severity": "medium"},
  {"kind": "contract-change", "title": "bump the bundle", "severity": "low", "body": "why"}
]` + "\n```\n\nSTATUS: DONE\n"
	report := ParseReport(body)
	if report.FindingsProblem != "" || len(report.Findings) != 4 || len(report.FindingsFolded) != 0 {
		t.Fatalf("findings %+v, problem %q, folded %v", report.Findings, report.FindingsProblem, report.FindingsFolded)
	}
	want := []Finding{
		{Kind: FindingKindDefect, Title: "a defect here", Body: "evidence: internal/x.go:42", Severity: "high", DoneItem: "A3", DemonstratingCheck: "go"},
		{Kind: FindingKindUpstreamTick, Title: "a tick for ticks", Severity: "low", Target: "pengelbrecht/ticks"},
		{Kind: FindingKindProposedTick, Title: "a tick for this repo", Severity: "medium"},
		{Kind: FindingKindContract, Title: "bump the bundle", Body: "why", Severity: "low"},
	}
	for i := range want {
		if report.Findings[i] != want[i] {
			t.Errorf("findings[%d] = %+v, want %+v", i, report.Findings[i], want[i])
		}
	}
	if !report.FindingsPresent || report.FindingsVersion != FindingsV2 {
		t.Errorf("present %v version %d", report.FindingsPresent, report.FindingsVersion)
	}
	if lint := LintReport(body, LintContext{Role: "implement-tick", AcceptanceItems: []string{"A3"}, CommandIDs: []string{"go"}}); !lint.Clean() {
		t.Errorf("a correct v2 report fails the check:\n%s", lint.Text())
	}
}

// An untagged block written in the v2 shape is read as v2: a worker that
// forgets the tag is not refused for it.
//
// short: pure parsing.
func TestAnUntaggedV2ShapedBlockIsReadAsV2(t *testing.T) {
	t.Parallel()
	block := ReadFindingsBlock("```findings\n" + `[{"kind": "proposal", "title": "t", "severity": "low"}]` + "\n```\n")
	if block.Problem != "" || block.Version != FindingsV2 || len(block.Findings) != 1 ||
		block.Findings[0].Kind != FindingKindProposedTick {
		t.Fatalf("block %+v", block)
	}
}

// `[]` says "found nothing"; no block says nothing — the two read apart.
//
// short: pure parsing.
func TestAnEmptyBlockIsDistinguishableFromNoBlock(t *testing.T) {
	t.Parallel()
	empty := ParseReport("```findings v2\n[]\n```\n\nSTATUS: DONE\n")
	none := ParseReport("STATUS: DONE\n")
	if !empty.FindingsPresent || none.FindingsPresent {
		t.Fatalf("empty present %v, none present %v", empty.FindingsPresent, none.FindingsPresent)
	}
	if lint := LintReport("STATUS: DONE\n", LintContext{}); !lint.Clean() || len(lint.Warnings) == 0 {
		t.Fatalf("no block: want clean with a warning, got\n%s", lint.Text())
	}
}

// Every error names where and what is allowed — the list a worker acts on.
//
// short: pure parsing.
func TestTheCheckerSaysWhereAndWhatIsAllowed(t *testing.T) {
	t.Parallel()
	ctx := LintContext{Role: "review-epic", AcceptanceItems: []string{"A1", "A2"}, CommandIDs: []string{"go", "lint"}}
	cases := []struct {
		name, body string
		wants      []string
		fatal      bool
	}{
		{"no status", "prose\nStatus: complete\n", []string{"STATUS", "line 2", "DONE_WITH_CONCERNS"}, true},
		{"no verdict", "STATUS: DONE\n", []string{"REVIEW-VERDICT", "NOT READY"}, true},
		// epic-6in: a NOT READY the run cannot act on is pushed back — the
		// bare verdict 6in's review gave ("DONE (NOT READY)"), and one whose
		// reasons are prose with no blocking (high) finding.
		{"a bare NOT READY", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"high\"}]\n```\nREVIEW-VERDICT: NOT READY\nSTATUS: DONE\n",
			[]string{"REVIEW-VERDICT", "carries no detail", "what would make the epic ready"}, true},
		{"a NOT READY with no blocking finding", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"medium\"}]\n```\nREVIEW-VERDICT: NOT READY — the gate never ran\nSTATUS: DONE\n",
			[]string{"REVIEW-VERDICT", "blocking findings", "`high`"}, true},
		{"unparseable block", "```findings v2\n[{\"kind\": \n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings block", "report line 3"}, true},
		{"a v2 kind", "```findings v2\n[{\"kind\":\"upstream-tick\",\"title\":\"t\",\"severity\":\"low\"}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].kind", "defect, proposal, contract-change", `"upstream-tick"`}, false},
		{"none for a check", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A1\",\"check\":\"none\"}}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].breaks.check", "omit it"}, false},
		{"an item the epic lacks", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A7\"}}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].breaks.item", "A1, A2"}, false},
		{"a check that is prose", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A1\",\"check\":\"the unit tests\"}}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].breaks.check", "go, lint"}, false},
		{"a long title", "```findings v2\n[{\"kind\":\"defect\",\"title\":\"" + strings.Repeat("x", 81) + "\",\"severity\":\"low\"}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].title", "80"}, false},
		{"a v1 folded key", "```findings\n[{\"kind\":\"defect\",\"title\":\"t\",\"body\":\"\",\"severity\":\"low\",\"target\":\"\",\"title_note\":\"x\"}]\n```\nREVIEW-VERDICT: READY\nSTATUS: DONE\n",
			[]string{"findings[0].title_note"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lint := LintReport(tc.body, ctx)
			if lint.Clean() {
				t.Fatalf("the check passed:\n%s", tc.body)
			}
			text := lint.Text()
			for _, want := range tc.wants {
				if !strings.Contains(text, want) {
					t.Errorf("the check does not say %q:\n%s", want, text)
				}
			}
			fatal := false
			for _, e := range lint.Errors {
				fatal = fatal || e.Fatal
			}
			if fatal != tc.fatal {
				t.Errorf("fatal %v, want %v:\n%s", fatal, tc.fatal, text)
			}
		})
	}
}

// A NOT READY that says what would make the epic ready and names a blocking
// (high) finding is one the run can act on: the checker passes it, and
// collect reads it (epic-6in).
//
// short: pure parsing.
func TestANotReadyWithDetailAndABlockingFindingPasses(t *testing.T) {
	t.Parallel()
	body := "```findings v2\n[{\"kind\":\"defect\",\"title\":\"the image check is always skipped\",\"severity\":\"high\"}," +
		"{\"kind\":\"proposal\",\"title\":\"a polish\",\"severity\":\"low\"}]\n```\n" +
		"REVIEW-VERDICT: NOT READY — the declared-image check never runs in a container\nSTATUS: DONE\n"
	if lint := LintReport(body, LintContext{Role: "review-epic"}); !lint.Clean() {
		t.Fatalf("an actionable NOT READY was refused:\n%s", lint.Text())
	}
	if refusal := ReportRefusal("review-epic", ParseReport(body)); len(refusal) > 0 {
		t.Fatalf("collect refuses an actionable NOT READY: %v", refusal)
	}
	if refusal := ReportRefusal("review-epic", ParseReport("REVIEW-VERDICT: NOT READY\nSTATUS: DONE\n")); len(refusal) != 2 {
		t.Fatalf("collect reads a bare NOT READY with no blocking finding (%v): nothing can act on it", refusal)
	}
	// Only the review states a verdict; another role's NOT READY is ignored,
	// never pushed back as a review's.
	if lint := LintReport("REVIEW-VERDICT: NOT READY\nSTATUS: DONE\n", LintContext{Role: "implement-tick"}); !lint.Clean() {
		t.Fatalf("an implement report was held to the review's contract:\n%s", lint.Text())
	}
}

// A runnable command line is a valid check without being a declared id.
//
// short: pure parsing plus a PATH lookup.
func TestARunnableCommandIsAValidCheck(t *testing.T) {
	t.Parallel()
	body := "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A1\",\"check\":\"sh -c true\"}}]\n```\n\nSTATUS: DONE\n"
	if lint := LintReport(body, LintContext{AcceptanceItems: []string{"A1"}, CommandIDs: []string{"go"}}); !lint.Clean() {
		t.Fatalf("a runnable command was refused:\n%s", lint.Text())
	}
}

// The CLI reads the context from the checkout: the epic's items through the
// tick record's parent, the declared command ids from .tick/runners.toml.
//
// short: one temporary directory, no processes.
func TestTheLintReportCommandReadsTheJobsContext(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".tick", "issues", "ep.json"),
		`{"id":"ep","type":"epic","acceptance_criteria":"[A1] one thing\n[A2] another"}`)
	mustWrite(t, filepath.Join(repo, ".tick", "issues", "t1.json"), `{"id":"t1","type":"task","parent":"ep"}`)
	report := filepath.Join(repo, "RESULT.md")
	mustWrite(t, report, "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A5\"}}]\n```\n\nSTATUS: DONE\n")

	var out, errOut bytes.Buffer
	code := Main([]string{"lint-report", report, "--role", "implement-tick", "--tick", "t1", "--repo", repo}, nil, &out, &errOut)
	if code != ExitError || !strings.Contains(out.String(), "A1, A2") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}

	mustWrite(t, report, "```findings v2\n[{\"kind\":\"defect\",\"title\":\"t\",\"severity\":\"low\",\"breaks\":{\"item\":\"A2\"}}]\n```\n\nSTATUS: DONE\n")
	out.Reset()
	if code := Main([]string{"lint-report", "--repo=" + repo, report, "--tick", "t1"}, nil, &out, &errOut); code != ExitOK {
		t.Fatalf("exit %d on a clean report:\n%s", code, out.String())
	}
	if code := Main([]string{"lint-report", filepath.Join(repo, "missing.md")}, nil, &out, &errOut); code != ExitError {
		t.Fatalf("exit %d on a missing report", code)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var _ = fmt.Sprint
