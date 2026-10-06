package workerview

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// fixtureStart is the fixture's first message timestamp: the frames' clock
// starts there, so the ages a frame draws are the stream's own.
var fixtureStart = time.UnixMilli(1791143165572)

func assertFrame(t *testing.T, got []string, want string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.TrimPrefix(want, "\n") {
		t.Errorf("the frame drew\n%s\n\nwant\n%s", strings.Join(got, "\n"), strings.TrimPrefix(want, "\n"))
	}
}

func TestTheFrameDrawsTheWholeConversationNewestLast(t *testing.T) {
	m := fold(t, fixtureFrames(t), fixtureStart)
	got := Render(m, FrameOptions{Title: "hpk#9 · epic-43y", Width: 80, Now: fixtureStart.Add(15 * time.Second)})
	assertFrame(t, got, `
hpk#9 · epic-43y · faux/faux-1 · ended
9 commits, last 6s ago · 3 model calls · 2 tool calls · last write 15s ago · 75…
────────────────────────────────────────────────────────────────────────
› input     do the job
∴ thinking  The tick wants a step file; a bash round writes it.
▸ bash      echo working; sleep 2; echo stepped > step.txt
  ✓ bash    working
» steer     Write the report next.
∴ thinking  The steer asked for a report; write it.
▸ write     RESULT-hpk.md
  ✓ write   Successfully wrote to RESULT-hpk.md
∴ thinking  Both rounds landed.
✎ answer    watched and steered
────────────────────────────────────────────────────────────────────────
■ the worker process is exiting`)
}

func TestTheFrameShowsWhatIsRunningAndQueuedBelowTheRule(t *testing.T) {
	// The watcher attached mid-bash, saw its first output, then the
	// operator's steer queue behind the tool round.
	m := fold(t, fixtureFrames(t)[:3], fixtureStart)
	got := Render(m, FrameOptions{Title: "hpk#9 · epic-43y", Width: 80, Now: fixtureStart.Add(5 * time.Second)})
	assertFrame(t, got, `
hpk#9 · epic-43y · faux/faux-1 · running bash
2 commits, last 3s ago · 1 model call · 1 tool call · last bash 5s ago · 668 in…
────────────────────────────────────────────────────────────────────────
› input     do the job
∴ thinking  The tick wants a step file; a bash round writes it.
▸ bash      echo working; sleep 2; echo stepped > step.txt
────────────────────────────────────────────────────────────────────────
⋯ bash      5s  echo working; sleep 2; echo stepped > step.txt
  │ working
» 1 input(s) queued, placed after the running tool round`)
}

func TestAShortPaneDropsTheOldestLinesAndSaysHowMany(t *testing.T) {
	m := fold(t, fixtureFrames(t), fixtureStart)
	got := Render(m, FrameOptions{Title: "hpk#9", Width: 60, Height: 9, Now: fixtureStart.Add(15 * time.Second)})
	if len(got) != 9 {
		t.Fatalf("a 9-line pane drew %d lines:\n%s", len(got), strings.Join(got, "\n"))
	}
	if got[3] != "… 7 earlier line(s)" {
		t.Errorf("the elision line: %q", got[3])
	}
	// The newest conversation and the live part survive.
	if !strings.HasPrefix(got[6], "✎ answer") || !strings.HasPrefix(got[8], "■ the worker process") {
		t.Errorf("the newest lines were dropped:\n%s", strings.Join(got, "\n"))
	}
	for _, line := range got {
		if w := ansi.StringWidth(line); w > 60 {
			t.Errorf("a line is %d wide in a 60-wide pane: %q", w, line)
		}
	}
}

func TestAPaneTooShortForTheLivePartKeepsItsNewestLines(t *testing.T) {
	m := fold(t, fixtureFrames(t)[:3], fixtureStart)
	got := Render(m, FrameOptions{Title: "hpk#9", Width: 80, Height: 5, Now: fixtureStart.Add(5 * time.Second)})
	if len(got) != 5 {
		t.Fatalf("a 5-line pane drew %d lines:\n%s", len(got), strings.Join(got, "\n"))
	}
	if got[len(got)-1] != "» 1 input(s) queued, placed after the running tool round" {
		t.Errorf("the newest live line was dropped:\n%s", strings.Join(got, "\n"))
	}
}

func TestTheStreamingLineShowsTheTailAsTheModelWrites(t *testing.T) {
	m := New()
	at := fixtureStart
	for _, f := range []Frame{
		events(t, `{"type":"snapshot","entries":[],"tools":[],"inbox":[],"run":{"inputs":[1]},"agent":{},"usage":{"models":{},"tools":{}}}`),
		events(t, `{"type":"message_start","message":{"role":"assistant","content":[{"type":"thinking","thinking":"First I read the tick record, then the epic's acceptance, then the watch command, and only then do I decide where the worker view lives"}]}}`),
	} {
		if err := m.Apply(f, at); err != nil {
			t.Fatal(err)
		}
	}
	got := Render(m, FrameOptions{Title: "y03#17", Width: 60, Now: at})
	last := got[len(got)-1]
	if !strings.HasPrefix(last, "… thinking  …") || !strings.HasSuffix(last, "decide where the worker view lives") {
		t.Errorf("the streaming line is not the thinking's tail: %q", last)
	}
	if ansi.StringWidth(last) > 60 {
		t.Errorf("the streaming line overflows: %d", ansi.StringWidth(last))
	}
}

func TestTheHeartbeatLineCarriesCountsDeltasAndTheLastToolsAge(t *testing.T) {
	frames := fixtureFrames(t)
	before := fold(t, frames[:3], fixtureStart).Heartbeat
	after := fold(t, frames, fixtureStart).Heartbeat
	got := HeartbeatLine("hpk#9", after, &before, 2*time.Minute, fixtureStart.Add(15*time.Second))
	want := "hpk#9 heartbeat: 9 commits (+7 in 2m00s), last 6s ago · 3 model calls (+2) · last tool: write RESULT-hpk.md, 15s ago"
	if got != want {
		t.Errorf("heartbeat:\n got %q\nwant %q", got, want)
	}
	// The first line has no previous one to count from; a quiet worker's
	// line says so by its ages, not by a guess.
	first := HeartbeatLine("hpk#9", before, nil, 0, fixtureStart.Add(10*time.Minute))
	if first != "hpk#9 heartbeat: 2 commits, last 9m58s ago · 1 model call · last tool: bash echo working; sleep 2; echo stepped > step.txt, 10m00s ago" {
		t.Errorf("first heartbeat: %q", first)
	}
}

func TestPlainLinesAreTheFramesLinesUnclipped(t *testing.T) {
	long := strings.Repeat("x", 1000)
	line := PlainLine(Item{Kind: KindThinking, Text: "a\nmulti-line\n" + long})
	if !strings.HasPrefix(line, "∴ thinking  a multi-line xxx") || ansi.StringWidth(line) > plainWidth {
		t.Errorf("plain line: %q (%d wide)", line[:60], ansi.StringWidth(line))
	}
	if got := PlainLine(Item{Kind: KindToolResult, Tool: "bash", IsError: true, Text: "\n\nexit status 1\nmore"}); got != "  ✗ bash    exit status 1" {
		t.Errorf("an error result: %q", got)
	}
	if got := PlainLine(Item{Kind: KindToolResult, Tool: "write"}); got != "  ✓ write   (no output)" {
		t.Errorf("an empty result: %q", got)
	}
}

func TestAgeSpellsDurationsTheWayAWatcherReadsThem(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                  "0s",
		4 * time.Second:               "4s",
		130 * time.Second:             "2m10s",
		time.Hour + 4*time.Minute + 9: "1h04m",
	} {
		if got := Age(d); got != want {
			t.Errorf("Age(%s) = %q, want %q", d, got, want)
		}
	}
}
