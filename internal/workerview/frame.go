package workerview

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// The frame types both watch sockets send (the package doc spells the shape).
const (
	FrameEvents  = "events"
	FrameEnd     = "end"
	FrameState   = "state"
	FrameLog     = "log"
	FrameSteered = "steered"
	FrameError   = "error"
)

// Frame is one message of a watch stream: a line of the local steer socket's
// watch, or one WebSocket message of the cloud WorkerAgent's.
type Frame struct {
	Type string `json:"type"`
	// Events are pi-durable agent events, decoded lazily by the model.
	Events []json.RawMessage `json:"events,omitempty"`
	// Reason is an end frame's why.
	Reason string `json:"reason,omitempty"`
	// State is the cloud attempt's state (WorkerAgentState).
	State *AttemptState `json:"state,omitempty"`
	// Text is a log frame's text.
	Text string `json:"text,omitempty"`
	// Error is an error frame's text (a refused steer on the cloud socket).
	Error string `json:"error,omitempty"`
	// Submission is a steered frame's submission id.
	Submission *int64 `json:"submission,omitempty"`
	// OK is set only on the local socket's refusal line ({"ok":false,…}),
	// which is the steer protocol's reply shape, not a frame.
	OK *bool `json:"ok,omitempty"`
}

// AttemptState is the cloud WorkerAgent's state, as its watch socket's state
// frame carries it (cloudflare/src/worker-agent.ts WorkerAgentState). Only
// what the view draws is read.
type AttemptState struct {
	Phase    string  `json:"phase"`
	ExitCode *int    `json:"exit_code"`
	Detail   *string `json:"detail"`
	Model    *string `json:"model"`
	Branch   *string `json:"branch"`
	Harness  string  `json:"harness"`
}

// ParseFrame reads one frame. A line that is not a JSON object with a type
// is an error, never a silently empty frame: a watcher that drew nothing
// for a stream it could not read would look exactly like a quiet worker.
func ParseFrame(line []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("a watch frame is not JSON: %w", err)
	}
	if f.Type == "" && f.OK != nil && !*f.OK {
		return Frame{}, fmt.Errorf("the worker refused the watch: %s", f.Error)
	}
	if f.Type == "" {
		return Frame{}, fmt.Errorf("a watch frame carries no type: %.120s", line)
	}
	return f, nil
}

// maxFrameLine bounds one line of the local stream. A snapshot carries the
// whole conversation (every entry, the tool definitions included), so a long
// attempt's first frame is megabytes, not kilobytes.
const maxFrameLine = 256 * 1024 * 1024

// ReadFrames reads a line-framed watch stream (the local socket's) to its
// end, handing each frame to fn as it lands. It returns nil at a clean EOF,
// fn's error when fn refuses, or the read's or a malformed line's error.
func ReadFrames(r io.Reader, fn func(Frame) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxFrameLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		f, err := ParseFrame(line)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return scanner.Err()
}
