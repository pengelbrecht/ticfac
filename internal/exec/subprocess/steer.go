package subprocess

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// The stuck nudge as a STEER (epic 43y step 7, tick hpk): the Go half of the
// protocol the harness's steer server owns (harness/src/local/steer-socket.ts
// — THE PROTOCOL is pinned there), and the one behaviour the durable runner
// adds to the supervisor's stuck ladder. A CLI runner that appears stuck can
// only be interrupted and re-prompted, because its session lives inside a
// process nobody can talk to; a pi-durable worker's conversation is durable
// in the attempt's own storage, its live process owns that storage, and it
// listens on one Unix domain socket for exactly this: input placed after the
// current tool round, in the same conversation, no relaunch.
//
// So the supervisor's stuck nudge on a durable runner is:
//
//	steer once through the socket (this file), the runner LIVES;
//	still quiet for the window after that → the stop, as before.
//
// A steer that cannot be delivered (the socket missing, the harness gone, no
// ack in time) falls back to the CLI runner's path — interrupt and re-prompt
// with StuckArgv — because a stuck worker with a dead door is still a stuck
// worker, and the watch's job is to move it, not to hold the ladder for one
// mechanism.

// steerTimeout bounds the whole exchange: connect, request line, ack. A
// stuck worker is a worker the run is already paying the wall clock for, so
// the steer gets seconds, not patience — an unacknowledged steer falls back
// rather than hangs the watch.
const steerTimeout = 5 * time.Second

// steerRequest is the one request line. The harness's own client
// (steerOnce, the node suite) speaks the same shape; `requestId` rides to
// pi-durable's idempotent submit, so a retry of an unacknowledged steer
// cannot place the message twice.
type steerRequest struct {
	RequestID string `json:"requestId,omitempty"`
	Text      string `json:"text"`
}

// steerReply is the one reply line: `ok` once the steer is DURABLY admitted
// — the harness answers after pi-durable's submit has committed it, so an
// acknowledged steer survives the harness dying.
type steerReply struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"requestId,omitempty"`
	Error     string `json:"error,omitempty"`
}

// steerRunner submits one steer to a live durable runner and reports whether
// it was durably admitted. It never hangs: the whole exchange is bounded by
// [steerTimeout], and every way it can fail is an error the caller falls
// back from.
func steerRunner(sock, text, requestID string) error {
	conn, err := net.DialTimeout("unix", sock, steerTimeout)
	if err != nil {
		return fmt.Errorf("dial the steer socket at %s: %w", sock, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(steerTimeout)); err != nil {
		return fmt.Errorf("bound the steer exchange: %w", err)
	}
	raw, err := json.Marshal(steerRequest{RequestID: requestID, Text: text})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("send the steer: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("the steer socket did not answer in %s: %w", steerTimeout, err)
	}
	var reply steerReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("the steer reply is not the pinned shape: %w", err)
	}
	if !reply.OK {
		if reply.Error != "" {
			return fmt.Errorf("the harness refused the steer: %s", reply.Error)
		}
		return fmt.Errorf("the harness refused the steer")
	}
	return nil
}
