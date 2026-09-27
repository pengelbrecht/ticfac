package statusmodel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The session-log reader (tick b1t's signal): the runner's own turn log is
// the worker-liveness fact — pi appends on every turn, so the log's age is a
// precise, cheap answer to "is this worker taking turns", the thing a
// worktree's mtime could never say. These tests pin the layout the reader
// walks and the two facts it answers.

func TestMungeWorktreeMatchesTheRunnersOwnLayout(t *testing.T) {
	t.Parallel()
	// The real layout, read off a live session directory: a worktree at
	// /a/b/c keeps its sessions under "--a-b-c--".
	for worktree, want := range map[string]string{
		"/a/b/c":                    "--a-b-c--",
		"/Users/op/worktrees/wt-1":  "--Users-op-worktrees-wt-1--",
		"/private/var/folders/wt-2": "--private-var-folders-wt-2--",
	} {
		if got := mungeWorktree(worktree); got != want {
			t.Errorf("mungeWorktree(%q) = %q, want %q", worktree, got, want)
		}
	}
}

// writeSession writes one session file under the runner's own directory
// layout for a worktree.
func writeSession(t *testing.T, home, worktree, name string, lines []string) {
	t.Helper()
	dir := filepath.Join(home, sessionsDirName, mungeWorktree(worktree))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLogAnswersTheLastTurn(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := "/w/run-epic-2jn/tick-6dh/attempt-2"

	// An older session, one the worker finished long ago: the newest FILE
	// is the current session, and the reader must pick it by name — the
	// session files are named for when they started, so the newest name is
	// the live one.
	writeSession(t, home, worktree, "2026-09-27T03-00-00-000Z_old.jsonl", []string{
		`{"type":"session","timestamp":"2026-09-27T03:00:00.000Z","cwd":"` + worktree + `"}`,
		`{"type":"message","timestamp":"2026-09-27T03:10:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"finished the first try"}]}}`,
	})
	writeSession(t, home, worktree, "2026-09-27T04-08-08-530Z_current.jsonl", []string{
		`{"type":"session","timestamp":"2026-09-27T04:08:08.530Z","cwd":"` + worktree + `"}`,
		`{"type":"model_change","timestamp":"2026-09-27T04:08:08.649Z","provider":"example","modelId":"m"}`,
		`{"type":"message","timestamp":"2026-09-27T04:08:08.656Z","message":{"role":"user","content":[{"type":"text","text":"# implement-tick"}]}}`,
		`{"type":"message","timestamp":"2026-09-27T04:29:15.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"read the model package first"}]}}`,
		`{"type":"message","timestamp":"2026-09-27T04:29:20.000Z","message":{"role":"assistant","content":[{"type":"toolCall","toolCallId":"c1","toolName":"read","input":{}}]}}`,
		`{"type":"custom","timestamp":"2026-09-27T04:29:25.000Z"}`,
	})

	turn := SessionLog(home, worktree)
	if turn == nil {
		t.Fatal("the reader answered nothing for a session that stands")
	}
	wantAt, _ := time.Parse(time.RFC3339Nano, "2026-09-27T04:29:25.000Z")
	if !turn.At.Equal(wantAt) {
		t.Errorf("the log last grew at %s, want the LAST entry's stamp %s", turn.At, wantAt)
	}
	if turn.Summary != "assistant: read" {
		t.Errorf("the last turn reads %q, want the last message entry's tool call", turn.Summary)
	}

	// Silence is the caller's arithmetic: the age of the same fact against
	// a now the caller holds.
	now := wantAt.Add(45 * time.Second)
	if silence := now.Sub(turn.At); silence.Round(time.Second) != 45*time.Second {
		t.Errorf("the silence off the reader's stamp is %s, want 45s", silence)
	}
}

func TestSessionLogSummarizesTextFlatsToOneBoundedLine(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := "/w/one"
	long := strings.Repeat("reading the whole tree before writing ", 20)
	writeSession(t, home, worktree, "2026-09-27T04-00-00-000Z_s.jsonl", []string{
		`{"type":"message","timestamp":"2026-09-27T04:01:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.ReplaceAll(long, "\n", " ") + `"}]}}`,
	})
	turn := SessionLog(home, worktree)
	if turn == nil {
		t.Fatal("no answer for a session with a text turn")
	}
	if !strings.HasPrefix(turn.Summary, "assistant: reading the whole tree") {
		t.Errorf("the summary lost the text: %q", turn.Summary)
	}
	if len(turn.Summary) > 130 {
		t.Errorf("the summary is unbounded: %d chars", len(turn.Summary))
	}
}

func TestSessionLogAnswersNothingWhereNothingStands(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if turn := SessionLog(home, "/w/never-started"); turn != nil {
		t.Errorf("a worktree with no session directory answered %+v, want nil", turn)
	}
	if turn := SessionLog("", "/w/nowhere"); turn != nil {
		t.Errorf("an empty home answered %+v, want nil", turn)
	}
	if turn := SessionLog(home, ""); turn != nil {
		t.Errorf("an empty worktree answered %+v, want nil", turn)
	}

	// A directory whose newest file is not a session log is the honest
	// no-fact, and a log with no parseable line says nothing: silence
	// about silence is not a fact.
	worktree := "/w/broken"
	dir := filepath.Join(home, sessionsDirName, mungeWorktree(worktree))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-09-27T04-00-00-000Z_s.jsonl"), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if turn := SessionLog(home, worktree); turn != nil {
		t.Errorf("an unparseable session answered %+v, want nil", turn)
	}
}

func TestSessionLogReadsOnlyTheTailOfALongSession(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := "/w/long"
	var lines []string
	for i := 0; i < 8000; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"type":"message","timestamp":"2026-09-27T04:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"turn %d of a very long orientation phase"}]}}`, i))
	}
	lines = append(lines, `{"type":"custom","timestamp":"2026-09-27T05:29:41.000Z"}`)
	writeSession(t, home, worktree, "2026-09-27T03-00-00-000Z_s.jsonl", lines)

	turn := SessionLog(home, worktree)
	if turn == nil {
		t.Fatal("a long session answered nothing")
	}
	wantAt, _ := time.Parse(time.RFC3339Nano, "2026-09-27T05:29:41.000Z")
	if !turn.At.Equal(wantAt) {
		t.Errorf("the long session's last growth reads %s, want %s", turn.At, wantAt)
	}
	if !strings.HasPrefix(turn.Summary, "assistant: turn ") {
		t.Errorf("the tail's last message did not ride: %q", turn.Summary)
	}
}
