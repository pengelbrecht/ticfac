package cli

// The drill-in views (epic hn6, wave 4 — tick c2u), pinned headless: the
// tick view renders one tick's own story out of the status model, the feed
// view renders the whole event feed as a scrollable window, and the refusal
// line names its reason and the run's next step. No pty and no herdr —
// identity styles at a fixed width, the contract bundle's `dashboard` golden
// for the model.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// TestTickViewShowsTriesReasonsAndEvidence: enter on a tick opens its own
// story, out of the contract golden's model — the refused try's reason, the
// run's next step, the report summary and its diff stats, the gate evidence
// on the tick's heads, and the findings it drafted.
func TestTickViewShowsTriesReasonsAndEvidence(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	// The golden's last try of 46x stands in flight and states no next step
	// (the model derives one only behind a refused last try); the run's own
	// next-step word is the one field the test shapes, so the "— next:"
	// rendering is pinned on the golden's own tick.
	if tick := watchTickOf(m, "46x"); tick == nil || len(tick.Tries) == 0 {
		t.Fatal("the dashboard golden carries no 46x tries to drill into")
	} else {
		tick.Tries[len(tick.Tries)-1].NextStep = ptr("retrying (try 3)")
	}

	view := strings.Join(renderTickView(m, "46x", plainStyles(), 100, 0), "\n")
	for _, want := range []string{
		"46x  port sandbox verbs to ticfac",
		"●gate", // the pipeline cell, the same one the dashboard renders
		"try 1", "strong", "gate-failed",
		"attempt 4 of 46x struck out: gofmt drifted in two files",
		"try 2", "frontier", "in-flight",
		"— next: retrying (try 3)",
		"report not read",
		"go  fail  4d8c0e9a",
		"[esc] back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the tick view does not carry %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "findings:") {
		t.Errorf("the tick view renders a findings section for a tick that drafted none:\n%s", view)
	}

	// 060's story: its report, its gate, its finding.
	view = strings.Join(renderTickView(m, "060", plainStyles(), 100, 0), "\n")
	for _, want := range []string{
		"060  cloud worker early-exit nudge",
		"try 1", "closed",
		"re-prompt wired; claude runs with background tasks off",
		"diff: 11 files +214 −60",
		"go  pass  1a2b3c4d",
		"efdc8f82", // the finding's durable key, the word triage addresses
		"gating",
		"the boot strap leaves the re-prompt off for herdr panes",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the tick view does not carry %q:\n%s", want, view)
		}
	}

	// A tick the plan no longer carries is said, never rendered as a wall of
	// nothing: the cursor can outlive a replan by one key.
	view = strings.Join(renderTickView(m, "zzz", plainStyles(), 100, 0), "\n")
	if !strings.Contains(view, "zzz is not in the epic's plan any more") {
		t.Errorf("a tick gone from the plan renders as something else:\n%s", view)
	}
}

// TestTickViewSaysReportNotRead: a tick whose report was not read says so —
// "report not read" is a claim about the reading, not a silence a person
// reads as "the report said nothing".
func TestTickViewSaysReportNotRead(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	view := strings.Join(renderTickView(m, "46x", plainStyles(), 100, 0), "\n")
	if !strings.Contains(view, "report not read") {
		t.Errorf("a tick with no read report does not say so:\n%s", view)
	}
	if strings.Contains(view, "diff:") {
		t.Errorf("a tick with no read report still shows diff stats:\n%s", view)
	}

	// A height too small keeps the footer and counts the rest, never
	// silently dropping lines.
	tall := renderTickView(m, "46x", plainStyles(), 100, 3)
	if len(tall) != 3 || tall[len(tall)-1] != "[esc] back" {
		t.Errorf("a 3-line pane did not keep the footer and count the rest:\n%s", strings.Join(tall, "\n"))
	}
}

// TestFeedViewScrolls: the full feed opens newest at the bottom and scrolls
// up in whole events — the window of 30 events at height 10 scrolled by 5 is
// the 16th through 25th oldest, nothing older and nothing newer in it.
func TestFeedViewScrolls(t *testing.T) {
	t.Parallel()
	events := make([]runfeed.Event, 0, 30)
	at := time.Date(2026, 9, 28, 19, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		n := i + 1
		events = append(events, runfeed.NewEvent(at.Add(time.Duration(i)*time.Minute), "epic-rmod",
			"t1", &n, reconcile.StageDispatched, fmt.Sprintf("event %d", n)))
	}
	var tries runfeed.Tries
	for _, event := range events {
		tries.Observe(event)
	}

	view := renderFeedView(events, &tries, nil, 5, 100, 10)
	joined := strings.Join(view, "\n")
	if len(view) != 10 {
		t.Errorf("a 10-line window rendered %d lines:\n%s", len(view), joined)
	}
	for _, want := range []string{"event 16", "event 25", "t1#16"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the scrolled window does not carry %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"event 15", "event 26"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("the scrolled window shows %q, outside its 10:\n%s", unwanted, joined)
		}
	}

	// Scroll 0 is the newest end.
	view = renderFeedView(events, &tries, nil, 0, 100, 10)
	joined = strings.Join(view, "\n")
	if !strings.Contains(joined, "event 30") || strings.Contains(joined, "event 20") {
		t.Errorf("an unscrolled feed is not the newest end:\n%s", joined)
	}

	// An unknown height is the whole feed, and a feed shorter than the
	// window is all of it, without padding.
	if got := len(renderFeedView(events, &tries, nil, 0, 100, 0)); got != 30 {
		t.Errorf("an unknown height rendered %d of the feed's 30 lines", got)
	}
	if got := len(renderFeedView(events[:4], &tries, nil, 0, 100, 10)); got != 4 {
		t.Errorf("a feed shorter than the window rendered %d of its 4 lines", got)
	}
}

// TestRejectedEventLineNamesReasonAndNextStep: the first-use bug ("v7z
// rejected x shows no reason and no next step"), pinned. Where the model is
// at hand, a refusal's line names its reason — the run's own detail, cut to
// its first clause — and the run's own next step from the tick's last try;
// without the model, the reason still shows and no next step is invented;
// and the old full-detail line form is unchanged byte for byte.
func TestRejectedEventLineNamesReasonAndNextStep(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	if tick := watchTickOf(m, "t2"); tick == nil || len(tick.Tries) < 2 {
		t.Fatal("the dashboard fixture carries no t2 tries to refuse")
	} else {
		tick.Tries[1].Outcome = statusmodel.TryRejected
		tick.Tries[1].Reason = ptr("no-commits: attempt 2 of t2 left no branch")
		tick.Tries[1].NextStep = ptr("the run will retry or escalate the tier")
	}
	at := time.Date(2026, 9, 28, 19, 4, 0, 0, time.UTC)
	first, second := 1, 2
	dispatched := runfeed.NewEvent(at, "epic-rmod", "t2", &first,
		reconcile.StageDispatched, "t2 try 1 dispatched")
	rejected := runfeed.NewEvent(at.Add(time.Minute), "epic-rmod", "t2", &second,
		reconcile.StageRejected, "no-commits: attempt 2 of t2 left no branch; the work is released")
	var tries runfeed.Tries
	tries.Observe(dispatched)
	tries.Observe(rejected)

	line := watchEventLineWith(rejected, &tries, &m)
	for _, want := range []string{"t2#2", "rejected: no-commits", "— next: the run will retry or escalate the tier"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused line does not name %q: %q", want, line)
		}
	}
	if strings.Contains(line, "left no branch; the work is released") {
		t.Errorf("the refused line carries the whole detail instead of its first clause: %q", line)
	}

	// No model at hand: the reason still shows, no next step is invented.
	line = watchEventLineWith(rejected, &tries, nil)
	if !strings.Contains(line, "rejected: no-commits") {
		t.Errorf("the refused line lost its reason without the model: %q", line)
	}
	if strings.Contains(line, "— next:") {
		t.Errorf("a next step was invented without the model: %q", line)
	}

	// A gate failure names its reason the same way.
	gateFailed := runfeed.NewEvent(at.Add(2*time.Minute), "epic-rmod", "t2", &second,
		reconcile.StageGateFailed, "the integrated gate did not pass: go test failed")
	line = watchEventLineWith(gateFailed, &tries, &m)
	if !strings.Contains(line, "gate_failed: the integrated gate did not pass") {
		t.Errorf("the gate-failure line does not name its reason: %q", line)
	}

	// Anything else is the old line, byte for byte — the stream path's words.
	if got, want := watchEventLineWith(dispatched, &tries, &m), watchEventLine(dispatched, &tries); got != want {
		t.Errorf("a non-refusal line changed form with the model at hand:\n%q\nwant %q", got, want)
	}
	if got := watchEventLine(rejected, &tries); !strings.Contains(got, "no-commits: attempt 2 of t2 left no branch; the work is released") {
		t.Errorf("the old full-detail line form changed: %q", got)
	}
}

// TestWatchEventReasonIsTheFirstClause: the clause the refusal line names is
// the detail up to the first ":" — or its first 120 characters, cut at a
// word boundary, when the detail carries no colon to cut at.
func TestWatchEventReasonIsTheFirstClause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		detail string
		want   string
	}{
		{"verdict-led", "no-commits: the branch is empty", "no-commits"},
		{"sentence with a colon", "attempt 4 of 46x struck out: gofmt drifted in two files", "attempt 4 of 46x struck out"},
		{"no colon, short", "the integrated gate refused attempt 4 of 46x (go)", "the integrated gate refused attempt 4 of 46x (go)"},
		{"no colon, long", strings.Repeat("word ", 40), strings.TrimRight(strings.Repeat("word ", 24), " ")}, // the boundary falls on a space: 24 whole words
		{"empty", "", ""},
	} {
		if got := watchEventReason(tc.detail); got != tc.want {
			t.Errorf("%s: watchEventReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
