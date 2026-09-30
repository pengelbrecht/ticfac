package runsignal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// The heartbeat of a LOCAL orchestrator (`ticfac run <epic> --cloud-workers`).
//
// A container orchestrator's liveness is the platform's answer about its
// process; an orchestrator on the operator's machine has no platform to ask,
// so it says so itself: every minute it POSTs the factory's heartbeat door on
// its run's own token (cloudflare/src/local-orchestrator.ts). The Run Workflow
// ends a run whose heartbeat went stale — which is what reclaims the worker
// containers of a machine that crashed or slept — and the door's answer
// carries the run's state back, so a stop asked of the factory reaches the
// machine at its next beat.
//
// The beat is a goroutine of its own, so a reconciler busy in a long gate
// still beats. What it does about an answer is narrow: a run the factory says
// is stopping, or a credential it no longer honours (revoked, the run over),
// is a run this process must not keep driving — it calls onStop once, and the
// caller flushes and exits the way a container's SIGTERM does. Everything
// else — a network blip, a 5xx — is said once per failing streak and retried
// at the next beat: the factory's staleness bound, not one lost POST, decides.

const heartbeatPath = "/api/heartbeat"

// EnvOrchestrator names where the run's orchestrator runs when it is not the
// factory's own container; OrchestratorLocal is `ticfac run --cloud-workers`.
// Spelled here and in internal/exec/cloudflaresandbox, pinned together by a
// test there.
const (
	EnvOrchestrator   = "TICKS_ORCHESTRATOR"
	OrchestratorLocal = "local"
)

// HeartbeatInterval is how often a local orchestrator beats: far inside the
// factory's fifteen-minute staleness bound, so a run survives several lost
// beats in a row.
const HeartbeatInterval = time.Minute

// Heartbeat beats one factory's heartbeat door until stopped. A nil
// *Heartbeat is valid and does nothing — HeartbeatFromEnv's answer for every
// run that is not a local orchestrator.
type Heartbeat struct {
	url      string
	token    string
	log      io.Writer
	http     *http.Client
	interval time.Duration
	onStop   func(reason string)

	stopOnce sync.Once
	stopped  sync.Once
	stop     chan struct{}
	done     chan struct{}
	failing  string
}

// HeartbeatFromEnv builds the heartbeat for a local orchestrator — the
// factory URL, the run token and EnvOrchestrator=local, all three set by
// `ticfac run --cloud-workers` — or nil for every other run.
func HeartbeatFromEnv(log io.Writer, onStop func(reason string)) *Heartbeat {
	if strings.TrimSpace(os.Getenv(EnvOrchestrator)) != OrchestratorLocal {
		return nil
	}
	url := strings.TrimSpace(os.Getenv(factoryURLEnv))
	token := strings.TrimSpace(os.Getenv(factoryTokenEnv))
	if url == "" || token == "" {
		return nil
	}
	return NewHeartbeat(url, token, HeartbeatInterval, log, onStop)
}

// NewHeartbeat builds a heartbeat against one factory.
func NewHeartbeat(factoryURL, token string, interval time.Duration, log io.Writer, onStop func(string)) *Heartbeat {
	if log == nil {
		log = io.Discard
	}
	if onStop == nil {
		onStop = func(string) {}
	}
	return &Heartbeat{
		url:      strings.TrimRight(strings.TrimSpace(factoryURL), "/"),
		token:    strings.TrimSpace(token),
		log:      log,
		http:     httpnet.Client(httpTimeout),
		interval: interval,
		onStop:   onStop,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start beats once now and then every interval, on its own goroutine.
func (h *Heartbeat) Start() {
	if h == nil {
		return
	}
	go h.loop()
}

// Stop ends the beating and waits for the goroutine to leave.
func (h *Heartbeat) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stop) })
	<-h.done
}

func (h *Heartbeat) loop() {
	defer close(h.done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		if !h.Beat(context.Background()) {
			return
		}
		select {
		case <-h.stop:
			return
		case <-ticker.C:
		}
	}
}

// heartbeatAnswer is the door's 200 body.
type heartbeatAnswer struct {
	RunID    string `json:"run_id"`
	State    string `json:"state"`
	Stopping bool   `json:"stopping"`
}

// heartbeatRefusal is the door's 4xx body.
type heartbeatRefusal struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

// Beat makes one beat and acts on the answer. It reports whether beating
// should go on: false once the factory has said this run is over for this
// process (onStop has then been called) or that it has no heartbeat to take.
func (h *Heartbeat) Beat(ctx context.Context) bool {
	if h == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+heartbeatPath, strings.NewReader("{}"))
	if err != nil {
		h.fail(fmt.Sprintf("could not build the request: %v", err))
		return true
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		h.fail(fmt.Sprintf("the factory could not be reached: %v", err))
		return true
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	switch {
	case resp.StatusCode == http.StatusOK:
		h.recovered()
		var answer heartbeatAnswer
		if err := json.Unmarshal(body, &answer); err == nil && answer.Stopping {
			h.halt("the factory is stopping this run (a stop was requested there)")
			return false
		}
		return true
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		var refusal heartbeatRefusal
		_ = json.Unmarshal(body, &refusal)
		h.halt(fmt.Sprintf("the factory no longer honours this run's credential (%d %s: %s)",
			resp.StatusCode, refusal.Error, refusal.Detail))
		return false
	case resp.StatusCode == http.StatusConflict:
		var refusal heartbeatRefusal
		_ = json.Unmarshal(body, &refusal)
		fmt.Fprintf(h.log, "heartbeat: the factory takes no heartbeat for this run (%s: %s); not beating\n",
			refusal.Error, refusal.Detail)
		return false
	default:
		h.fail(fmt.Sprintf("the factory answered %d", resp.StatusCode))
		return true
	}
}

func (h *Heartbeat) halt(reason string) {
	h.stopped.Do(func() {
		fmt.Fprintf(h.log, "heartbeat: %s — this run stops here\n", reason)
		h.onStop(reason)
	})
}

// fail says a failing streak once; recovered says its end.
func (h *Heartbeat) fail(message string) {
	if h.failing == "" {
		fmt.Fprintf(h.log, "heartbeat: %s; beating again in %s (the factory ends the run only after many missed beats)\n",
			message, h.interval)
	}
	h.failing = message
}

func (h *Heartbeat) recovered() {
	if h.failing != "" {
		fmt.Fprintf(h.log, "heartbeat: the factory answers again\n")
	}
	h.failing = ""
}
