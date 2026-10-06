package statusmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The runner's own session log is the worker-liveness signal tick b1t names:
// pi appends one JSON line per turn, so the log's growth is a precise, cheap
// fact about whether the worker is taking turns — the thing the worktree's
// mtime could not say, because a worker thinking hard legitimately writes
// nothing for a long while (the 15-40 minute orientation phase that got three
// working attempts killed by hand).
//
// Where the log lives is the runner's own layout, stated here once so the
// reader and the path it reads cannot drift apart silently:
// ~/.pi/agent/sessions/<munged worktree>/<session>.jsonl, where the munged
// name is the worktree's absolute path with every "/" a "-", wrapped in one
// leading and one trailing dash (so the path's own leading "/" doubles). The
// session file is named for when the session started; a worktree's CURRENT
// session is the newest file in its directory.
//
// This reader measures and summarizes; it decides nothing. Silence is a
// reason to look, never a stop — b1t's own corollary, recorded after two
// healthy workers went quiet for seventeen minutes and resumed on their own.

// sessionsDirName is the sessions directory under the runner's root.
const sessionsDirName = ".pi/agent/sessions"

// tailBytes is how much of a session log the reader reads: the newest turn
// is always at the end, and a session can grow past what any status read
// should haul into memory. 256 KiB is thousands of turns.
const tailBytes = 256 * 1024

// mungeWorktree is the runner's own directory spelling for one worktree
// path, read off its live layout: the path with a trailing separator, every
// "/" a "-", wrapped in one dash on each side (so the path's own leading
// "/" doubles at both ends).
func mungeWorktree(worktree string) string {
	return "-" + strings.ReplaceAll(worktree+"/", "/", "-") + "-"
}

// SessionDir is where one worktree's runner sessions live under home — the
// layout the reader walks, stated once so a test that fakes a home cannot
// drift from it.
func SessionDir(home, worktree string) string {
	return filepath.Join(home, sessionsDirName, mungeWorktree(worktree))
}

// SessionLog answers one worktree's runner session: when its log last grew
// and a one-line summary of the last turn in it. The home is the runner's
// home directory (where ~/.pi lives), passed in rather than reached for so
// a test reads a fixture. Nil — the honest no-fact — when the worktree has
// no session directory, no session file, or no line that parses.
func SessionLog(home, worktree string) *Turn {
	if home == "" || worktree == "" {
		return nil
	}
	dir := SessionDir(home, worktree)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	newest := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if newest == "" || e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return nil
	}
	path := filepath.Join(dir, newest)
	raw, err := readTail(path, tailBytes)
	if err != nil {
		return nil
	}
	return lastTurn(raw)
}

// readTail reads the last bound bytes of a file, aligned forward to the next
// newline so only whole lines parse. A file smaller than the bound reads
// whole.
func readTail(path string, bound int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= bound {
		raw := make([]byte, size)
		if _, err := file.ReadAt(raw, 0); err != nil {
			return nil, err
		}
		return raw, nil
	}
	raw := make([]byte, bound)
	if _, err := file.ReadAt(raw, size-bound); err != nil {
		return nil, err
	}
	if index := bytes.IndexByte(raw, '\n'); index >= 0 {
		raw = raw[index+1:]
	}
	return raw, nil
}

// sessionEntry is one line of a session log, as far as this reader reads
// it: the entry's own type and stamp, and — on a message entry — the
// message's role and content blocks.
type sessionEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   *sessionMessage `json:"message"`
}

type sessionMessage struct {
	Role    string         `json:"role"`
	Content []sessionBlock `json:"content"`
}

type sessionBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Thinking is the thinking block's own payload; Name is the tool a
	// toolCall block names (toolResult carries the tool as ToolName) —
	// both read as they appear in the log, never guessed.
	Thinking string `json:"thinking"`
	Name     string `json:"name"`
	ToolName string `json:"toolName"`
}

// lastTurn reads the tail's lines and answers the two facts: the stamp of
// the LAST entry (any entry — every append is the runner being alive) and a
// one-line summary of the last MESSAGE entry (what the runner was last seen
// doing). Both empty — the caller's nil — when no line parses: a log that
// cannot be read says nothing, and silence about silence is not a fact.
func lastTurn(raw []byte) *Turn {
	var at time.Time
	var summary string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry sessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if stamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			at = stamp
		}
		if entry.Type == "message" && entry.Message != nil {
			summary = summarizeTurn(entry.Message)
		}
	}
	if at.IsZero() && summary == "" {
		return nil
	}
	return &Turn{At: at, Summary: summary}
}

// summarizeTurn renders one message entry as a person reads it: the role and
// the first thing it did — the text it said, the thought it opened, the tool
// it called or the tool that answered. One line, bounded, never a parse.
func summarizeTurn(message *sessionMessage) string {
	if len(message.Content) == 0 {
		return message.Role
	}
	block := message.Content[0]
	what := ""
	switch block.Type {
	case "text":
		// The whole line stays bounded: the role rides inside the same
		// 120-character budget, not on top of it.
		budget := 120 - len(message.Role) - 2
		if budget < 20 {
			budget = 20
		}
		what = oneLine(block.Text, budget)
	case "thinking":
		budget := 120 - len(message.Role) - 2
		if budget < 20 {
			budget = 20
		}
		if snippet := oneLine(block.Thinking, budget); snippet != "" {
			what = "thinking: " + snippet
		} else {
			what = "thinking"
		}
	case "toolCall", "tool_use":
		what = orFirst(block.Name, block.ToolName)
		if what == "" {
			what = "a tool call"
		}
	default:
		what = block.Type
	}
	if what == "" {
		what = block.Type
	}
	return fmt.Sprintf("%s: %s", message.Role, what)
}

// orFirst is the first non-empty of two names a block might carry its tool
// in — the log's own spellings, whichever one it used.
func orFirst(first, second string) string {
	if first != "" {
		return first
	}
	return second
}

// oneLine flattens a snippet to a single bounded line: bound CHARACTERS, cut
// on a rune boundary — a byte cut splits a multi-byte rune into invalid
// UTF-8, and the line rides JSON a renderer prints. The ellipsis is inside
// the bound: the whole line stays within it, the way boundLine (the action
// line in internal/exec/subprocess) cuts its own 80.
func oneLine(text string, bound int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= bound {
		return text
	}
	return string(runes[:bound-1]) + "…"
}
