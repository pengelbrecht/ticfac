package workerview

import (
	"encoding/json"
	"strings"
)

// The pi-durable agent events this package reads (harness/node_modules/
// @earendil-works/pi-durable/dist/harness/events.d.ts, pinned at the harness
// package's exact version). One struct holds every variant's fields: the
// type says which are set. `message` is an object on message_start and a
// string on task_failed, so it stays raw until the type is known.
type event struct {
	Type string `json:"type"`

	// snapshot
	Entries    []entry         `json:"entries"`
	Generation *generation     `json:"generation"`
	Tools      []toolSlot      `json:"tools"`
	Inbox      []queuedItem    `json:"inbox"`
	Agent      *agentState     `json:"agent"`
	Usage      *usageState     `json:"usage"`
	Run        *struct{}       `json:"run"`
	Message    json.RawMessage `json:"message"`

	// message_update
	Changes []change `json:"changes"`

	// message_end, entry_appended, tool_execution_end
	Entry *entry `json:"entry"`

	// tool_execution_*
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Output     *outputChange   `json:"output"`

	// inbox_update
	Items []queuedItem `json:"items"`

	// submission
	Record *submission `json:"record"`

	// auto_retry_start, task_failed
	Attempt      int    `json:"attempt"`
	ErrorMessage string `json:"errorMessage"`
	Kind         string `json:"kind"`

	// compaction_*
	Reason string `json:"reason"`
}

type entry struct {
	ID    int64     `json:"id"`
	Kind  string    `json:"kind"`
	Model []message `json:"model"`
}

type generation struct {
	Attempt int      `json:"attempt"`
	Message *message `json:"message"`
	Retry   *struct {
		Error string `json:"error"`
	} `json:"retry"`
}

type toolSlot struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output"`
}

type queuedItem struct {
	ID   int64  `json:"id"`
	Mode string `json:"mode"`
}

type agentState struct {
	Model *struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	} `json:"model"`
}

type usageState struct {
	Models map[string]usage `json:"models"`
}

type usage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

type submission struct {
	ID        int64  `json:"id"`
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
	Entry     *int64 `json:"entry"`
}

type outputChange struct {
	TrimStart int     `json:"trimStart"`
	Append    string  `json:"append"`
	Set       *string `json:"set"`
}

// change is one change to the in-flight assistant message (MessageChange).
type change struct {
	Type         string          `json:"type"`
	ContentIndex int             `json:"contentIndex"`
	Block        *block          `json:"block"`
	Delta        string          `json:"delta"`
	Message      *message        `json:"message"`
	Path         json.RawMessage `json:"path"`
}

// message is a pi-ai Message: user, assistant, toolResult or system.
type message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	IsError      bool            `json:"isError"`
	Timestamp    int64           `json:"timestamp"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
}

// block is one content block: text, thinking, toolCall or image.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Redacted  bool            `json:"redacted"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// argsText accumulates a streaming tool call's argument deltas, which
	// arrive as JSON text before the arguments object is whole.
	argsText string
}

// blocks reads a message's content, whichever shape it takes: a user
// message's content may be a plain string.
func (m message) blocks() []block {
	if len(m.Content) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return []block{{Type: "text", Text: text}}
	}
	var out []block
	if err := json.Unmarshal(m.Content, &out); err != nil {
		return nil
	}
	return out
}

// text joins a message's text blocks.
func (m message) text() string {
	var parts []string
	for _, b := range m.blocks() {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
