package workerview

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Kind is what one item of the conversation is.
type Kind string

const (
	// KindInput is input the conversation was given: the job prompt, or a
	// relaunch's follow-up (a nudge, a report pushback).
	KindInput Kind = "input"
	// KindSteer is input placed into a RUNNING conversation: an operator's
	// `ticfac steer`, or the supervisor's stuck nudge.
	KindSteer Kind = "steer"
	// KindThinking is the model's reasoning.
	KindThinking Kind = "thinking"
	// KindText is the model's answer text.
	KindText Kind = "text"
	// KindToolCall is a tool the model called, with its arguments summarised.
	KindToolCall Kind = "tool_call"
	// KindToolResult is what a tool answered.
	KindToolResult Kind = "tool_result"
	// KindNote is the harness's own word: a retry, a failed task, a
	// compaction, a model error.
	KindNote Kind = "note"
)

// Item is one settled thing in the conversation, in conversation order.
type Item struct {
	Kind Kind
	// Text is the body: the input, the thinking, the answer, the tool's
	// output, the note.
	Text string
	// Tool and CallID name a tool call or result.
	Tool   string
	CallID string
	// Args is a tool call's arguments, summarised to the one that says what
	// the call does (the command, the path), or compact JSON.
	Args string
	// IsError is a tool result that failed, or a note about a failure.
	IsError bool
	// At is when the message was made (its own timestamp); zero when the
	// stream did not say.
	At time.Time

	// entry is an input's conversation entry, so a steer submission that
	// lands after it can relabel it.
	entry int64
}

// ToolRun is a tool call that is running now, with its live output.
type ToolRun struct {
	CallID string
	Name   string
	Args   string
	Output string
	// Seen is when this watcher first saw it running (the stream carries no
	// start time).
	Seen time.Time
}

// Heartbeat is f6o's activity line, from the commit sequence: what a watcher
// needs to tell a busy worker from a stuck one.
type Heartbeat struct {
	// Commits is how many commits this watcher has seen since it attached
	// (every events frame after the snapshot is one).
	Commits int
	// LastCommit is when the newest of them landed; zero before the first.
	LastCommit time.Time
	// ModelCalls is the conversation's settled assistant messages — one per
	// model call — snapshot included.
	ModelCalls int
	// ToolCalls is the conversation's tool calls, snapshot included.
	ToolCalls int
	// LastTool is the newest tool call, nil before the first.
	LastTool *Item
	// LastActivity is the newest message timestamp the conversation holds,
	// or the newest commit — whichever is later: the age a stuck watch reads.
	LastActivity time.Time
	// InputTokens and OutputTokens are the conversation's usage.
	InputTokens  int64
	OutputTokens int64
}

// Model is one worker's conversation, folded from its watch stream.
type Model struct {
	// Items are the settled conversation, in order.
	Items []Item
	// Running are the tools running now, in the order they started.
	Running []ToolRun
	// Queued is how many inputs wait in the inbox (steers not yet placed).
	Queued int
	// Retry is the provider error a generation is backing off from, "" when
	// none is.
	Retry string
	// Live says a run is going: an input is being worked on.
	Live bool
	// ModelName is the agent's model, provider/id.
	ModelName string
	// State is the cloud attempt's state, nil on a local stream.
	State *AttemptState
	// Ended is the end frame's reason, "" while the stream lasts.
	Ended string
	// Log is the cloud attempt's host log, newest last, bounded.
	Log []string

	Heartbeat Heartbeat

	attached     bool
	seen         map[int64]bool
	partial      []block        // the in-flight assistant message; nil between generations
	steerSubmits map[int64]bool // submission ids queued as steers
	steerEntries map[int64]bool // entry ids a steer submission placed
}

// New is an empty model, waiting for its snapshot.
func New() *Model {
	return &Model{seen: map[int64]bool{}, steerSubmits: map[int64]bool{}, steerEntries: map[int64]bool{}}
}

// maxLogLines bounds the host log the model keeps.
const maxLogLines = 200

// maxToolOutput bounds one running tool's retained output: the frame shows
// its last lines, and a build's whole log is not a frame's business.
const maxToolOutput = 64 * 1024

// Apply folds one frame into the model; now stamps the commit it carries.
func (m *Model) Apply(f Frame, now time.Time) error {
	switch f.Type {
	case FrameEvents:
		commit := m.attached
		for _, raw := range f.Events {
			var e event
			if err := json.Unmarshal(raw, &e); err != nil {
				return fmt.Errorf("an agent event is not the pinned shape: %w", err)
			}
			if e.Type == "snapshot" {
				// A snapshot mid-stream is pi-durable's overflow answer: it
				// replaced undelivered batches, so it is still a commit.
				commit = commit || m.attached
				m.attached = true
			}
			m.apply(e, now)
		}
		if commit {
			m.Heartbeat.Commits++
			m.Heartbeat.LastCommit = now
			m.touch(now)
		}
	case FrameState:
		m.State = f.State
	case FrameLog:
		for _, line := range strings.Split(strings.TrimRight(f.Text, "\n"), "\n") {
			if line != "" {
				m.Log = append(m.Log, line)
			}
		}
		if len(m.Log) > maxLogLines {
			m.Log = m.Log[len(m.Log)-maxLogLines:]
		}
	case FrameEnd:
		m.Ended = f.Reason
		if m.Ended == "" {
			m.Ended = "the watch ended"
		}
	}
	return nil
}

func (m *Model) apply(e event, now time.Time) {
	switch e.Type {
	case "snapshot":
		m.Items, m.Running, m.partial = nil, nil, nil
		m.seen = map[int64]bool{}
		m.Heartbeat.ModelCalls, m.Heartbeat.ToolCalls, m.Heartbeat.LastTool = 0, 0, nil
		for _, en := range e.Entries {
			m.settle(en)
		}
		if e.Generation != nil {
			if e.Generation.Message != nil {
				m.partial = e.Generation.Message.blocks()
			}
			m.Retry = ""
			if e.Generation.Retry != nil {
				m.Retry = e.Generation.Retry.Error
			}
		}
		for _, slot := range e.Tools {
			if slot.Status == "running" {
				m.Running = append(m.Running, ToolRun{
					CallID: slot.CallID, Name: slot.Name, Output: bound(slot.Output),
					Args: m.argsOf(slot.CallID), Seen: now,
				})
			}
		}
		m.Queued = len(e.Inbox)
		for _, q := range e.Inbox {
			if q.Mode == "steer" {
				m.steerSubmits[q.ID] = true
			}
		}
		m.Live = e.Run != nil
		if e.Agent != nil && e.Agent.Model != nil {
			m.ModelName = e.Agent.Model.Provider + "/" + e.Agent.Model.ModelID
		}
		m.usage(e.Usage)
	case "message_start":
		var msg message
		if json.Unmarshal(e.Message, &msg) == nil && msg.Role == "assistant" {
			m.partial = msg.blocks()
			if m.partial == nil {
				m.partial = []block{}
			}
		}
	case "message_update":
		m.partial = updateBlocks(m.partial, e.Changes)
	case "message_end", "entry_appended":
		if e.Entry != nil {
			if len(e.Entry.Model) > 0 && e.Entry.Model[0].Role == "assistant" {
				m.partial = nil
			}
			m.settle(*e.Entry)
		}
	case "tool_execution_start":
		m.Running = append(m.Running, ToolRun{
			CallID: e.ToolCallID, Name: e.ToolName, Args: summarizeArgs(e.ToolName, e.Args), Seen: now,
		})
	case "tool_execution_update":
		for i := range m.Running {
			if m.Running[i].CallID != e.ToolCallID || e.Output == nil {
				continue
			}
			run := &m.Running[i]
			switch {
			case e.Output.Set != nil:
				run.Output = *e.Output.Set
			default:
				if e.Output.TrimStart > 0 {
					run.Output = run.Output[min(e.Output.TrimStart, len(run.Output)):]
				}
				run.Output += e.Output.Append
			}
			run.Output = bound(run.Output)
		}
	case "tool_execution_end":
		kept := m.Running[:0]
		for _, run := range m.Running {
			if run.CallID != e.ToolCallID {
				kept = append(kept, run)
			}
		}
		m.Running = kept
		if e.Entry != nil {
			m.settle(*e.Entry)
		}
	case "inbox_update":
		m.Queued = len(e.Items)
		for _, q := range e.Items {
			if q.Mode == "steer" {
				m.steerSubmits[q.ID] = true
			}
		}
	case "submission":
		if r := e.Record; r != nil && r.Entry != nil && m.steerSubmits[r.ID] {
			m.steerEntries[*r.Entry] = true
			m.relabelSteer(*r.Entry)
		}
	case "run_start":
		m.Live = true
	case "run_end":
		m.Live = false
	case "auto_retry_start":
		m.Retry = e.ErrorMessage
		m.Items = append(m.Items, Item{Kind: KindNote, IsError: true, At: now,
			Text: fmt.Sprintf("the model call failed; retry %d: %s", e.Attempt, oneLine(e.ErrorMessage))})
	case "auto_retry_end":
		m.Retry = ""
	case "task_failed":
		var text string
		_ = json.Unmarshal(e.Message, &text)
		m.Items = append(m.Items, Item{Kind: KindNote, IsError: true, At: now,
			Text: fmt.Sprintf("%s failed: %s", e.Kind, oneLine(text))})
	case "compaction_start":
		m.Items = append(m.Items, Item{Kind: KindNote, At: now, Text: "compacting the context (" + e.Reason + ")"})
	case "usage_changed":
		m.usage(e.Usage)
	case "agent_changed":
		if e.Agent != nil && e.Agent.Model != nil {
			m.ModelName = e.Agent.Model.Provider + "/" + e.Agent.Model.ModelID
		}
	}
}

// settle appends one entry's items, once per entry id.
func (m *Model) settle(en entry) {
	if m.seen[en.ID] {
		return
	}
	m.seen[en.ID] = true
	for _, msg := range en.Model {
		at := time.Time{}
		if msg.Timestamp > 0 {
			at = time.UnixMilli(msg.Timestamp)
		}
		switch msg.Role {
		case "user":
			kind := KindInput
			if m.steerEntries[en.ID] {
				kind = KindSteer
			}
			m.Items = append(m.Items, Item{Kind: kind, Text: msg.text(), At: at, entry: en.ID})
		case "assistant":
			m.Heartbeat.ModelCalls++
			for _, b := range msg.blocks() {
				switch b.Type {
				case "thinking":
					text := b.Thinking
					if b.Redacted {
						text = "(redacted)"
					}
					if strings.TrimSpace(text) != "" {
						m.Items = append(m.Items, Item{Kind: KindThinking, Text: text, At: at})
					}
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						m.Items = append(m.Items, Item{Kind: KindText, Text: b.Text, At: at})
					}
				case "toolCall":
					call := Item{Kind: KindToolCall, Tool: b.Name, CallID: b.ID, Args: summarizeArgs(b.Name, b.Arguments), At: at}
					m.Items = append(m.Items, call)
					m.Heartbeat.ToolCalls++
					last := call
					m.Heartbeat.LastTool = &last
				}
			}
			if msg.StopReason == "error" && msg.ErrorMessage != "" {
				m.Items = append(m.Items, Item{Kind: KindNote, IsError: true, At: at,
					Text: "the model answered with an error: " + oneLine(msg.ErrorMessage)})
			}
		case "toolResult":
			m.Items = append(m.Items, Item{Kind: KindToolResult, Tool: msg.ToolName, CallID: msg.ToolCallID,
				Text: msg.text(), IsError: msg.IsError, At: at})
		}
		if at.After(m.Heartbeat.LastActivity) {
			m.Heartbeat.LastActivity = at
		}
	}
}

// relabelSteer marks an already-settled input as the steer it was: the
// submission that names its entry can land after the entry itself.
func (m *Model) relabelSteer(id int64) {
	for i := range m.Items {
		if m.Items[i].Kind == KindInput && m.Items[i].entry == id {
			m.Items[i].Kind = KindSteer
		}
	}
}

func (m *Model) touch(now time.Time) {
	if now.After(m.Heartbeat.LastActivity) {
		m.Heartbeat.LastActivity = now
	}
}

func (m *Model) usage(u *usageState) {
	if u == nil {
		return
	}
	var in, out int64
	keys := make([]string, 0, len(u.Models))
	for k := range u.Models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		in += u.Models[k].Input
		out += u.Models[k].Output
	}
	m.Heartbeat.InputTokens, m.Heartbeat.OutputTokens = in, out
}

// argsOf finds a settled call's argument summary, for a running slot the
// snapshot names only by call id.
func (m *Model) argsOf(callID string) string {
	for i := len(m.Items) - 1; i >= 0; i-- {
		if m.Items[i].Kind == KindToolCall && m.Items[i].CallID == callID {
			return m.Items[i].Args
		}
	}
	return ""
}

// PartialItems is the in-flight assistant message as items, block by block,
// while the model streams it; nil between generations.
func (m *Model) PartialItems() []Item {
	if m.partial == nil {
		return nil
	}
	out := []Item{}
	for _, b := range m.partial {
		switch b.Type {
		case "thinking":
			out = append(out, Item{Kind: KindThinking, Text: b.Thinking})
		case "text":
			out = append(out, Item{Kind: KindText, Text: b.Text})
		case "toolCall":
			// Streaming arguments are JSON text until the call is whole; the
			// text is the newer of the two while it grows.
			args := oneLine(b.argsText)
			if args == "" {
				args = summarizeArgs(b.Name, b.Arguments)
			}
			out = append(out, Item{Kind: KindToolCall, Tool: b.Name, CallID: b.ID, Args: args})
		}
	}
	return out
}

// updateBlocks applies message changes to the in-flight message's blocks.
func updateBlocks(blocks []block, changes []change) []block {
	if blocks == nil {
		blocks = []block{}
	}
	at := func(i int) *block {
		for len(blocks) <= i {
			blocks = append(blocks, block{})
		}
		return &blocks[i]
	}
	for _, c := range changes {
		switch c.Type {
		case "message":
			if c.Message != nil {
				blocks = c.Message.blocks()
			}
		case "text_start", "thinking_start", "toolcall_start", "block":
			if c.Block != nil {
				*at(c.ContentIndex) = *c.Block
			}
		case "text_delta":
			b := at(c.ContentIndex)
			b.Type, b.Text = "text", b.Text+c.Delta
		case "thinking_delta":
			b := at(c.ContentIndex)
			b.Type, b.Thinking = "thinking", b.Thinking+c.Delta
		case "toolcall_delta":
			b := at(c.ContentIndex)
			b.Type, b.argsText = "toolCall", b.argsText+c.Delta
		}
	}
	return blocks
}

// summarizeArgs is the one argument that says what a call does — the
// command, the path, the pattern — or the arguments as compact JSON.
func summarizeArgs(tool string, raw json.RawMessage) string {
	var args map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil || len(args) == 0 {
		return ""
	}
	for _, key := range []string{"command", "path", "file_path", "pattern", "query", "url"} {
		if v, ok := args[key].(string); ok && v != "" {
			return oneLine(v)
		}
	}
	compact, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(compact)
}

// oneLine folds whitespace runs (line breaks included) into single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// bound keeps the tail of a running tool's output.
func bound(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[len(s)-maxToolOutput:]
}
