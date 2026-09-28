package cli

// The local run's half of "follow ticfac from a phone" (ticfac tick i1r): the
// pusher that makes a local run visible to the factory's /status page while
// it works.
//
// A LOCAL run is invisible to the factory by construction — its tracker, its
// .ticfac/ records and its event feed are on the laptop that runs it — so the
// factory cannot compose its status the way it composes a cloud run's. The
// run therefore reports ITSELF: this pusher gathers the same status model
// `ticfac status --json` gathers (the one model every surface renders, tick
// 6dh) and POSTs it as a small snapshot to the factory's snapshot door
// (`POST /api/status-snapshots`), on a short cadence while the run works and
// once more at each ending, when the terminal record exists.
//
// Three rules shape it:
//
//   - OPT-IN. A run pushes only when the operator asked — the
//     `--status-push` flag on `run-epic`, whose DEFAULT reads the operator's
//     set-once preference from $TICFAC_STATUS_PUSH — and a factory is
//     configured. No opt-in, or no factory to push to, and the pusher is
//     nil and costs nothing. The tick named the opt-in "a ticfac config
//     flag": the flag is a flag, and its config is the $TICFAC_* environment
//     — the vocabulary this repository already owns for operator preferences
//     ($TICFAC_RUNNER defaults `--runner` the same way) — because ~/.ticfacrc's key vocabulary is the
//     pinned credential-ownership bundle's and closed ("an unknown
//     factory_ key is a typo"), so a `factory_status_push` key there is a
//     bundle re-cut away and is drafted as a finding. The flag on the command
//     line always wins over the environment, both ways.
//
//   - BEST-EFFORT, ALWAYS. The pusher can never slow, stop or fail the run it
//     reports: a push that fails is a line in run.log, and the next cadence
//     tries again. The remote view is a convenience; the run is the work.
//
//   - NO SECRETS, NO WORK PRODUCT. The snapshot carries the status model and
//     the label map the page names ticks by (tick q90) — nothing else. The
//     model is the same public answer `ticfac status --json` prints; worker
//     code, reports and credentials never ride it.
//
// A local run pauses when the laptop sleeps; the page says PAUSED/STALE when
// the snapshots stop, which is exactly the honest reading — the pusher does
// not try to be cleverer than its own silence.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// statusPushInterval is the push cadence: short enough that the page's
// staleness window (90s) means something, long enough that a run pushing every
// 30 seconds is one small write, not a conversation.
const statusPushInterval = 30 * time.Second

// statusSnapshotPath is the factory's snapshot door. The TS half pins the
// contract (cloudflare/src/status.ts): envelope version, the model's own
// `ticfac.status.v1` version, host "local", and the run identity matching its
// model.
const statusSnapshotPath = "/api/status-snapshots"

// statusPushEnvelopeVersion is the pushed-envelope shape's version. It moves
// only with the envelope; the model inside carries its own.
const statusPushEnvelopeVersion = 1

// StatusPushEnv is the config spelling of the remote-view opt-in: set it once
// on the machine runs start from and every `run-epic` follows, without the
// operator retyping the flag. Read as a strict bool (strconv.ParseBool):
// "1"/"true"/"t" opt in, "0"/"false"/"f" opt out, and anything that does not
// parse — including a typo — is off. The `--status-push` flag on the command
// line always overrides it, both ways.
const StatusPushEnv = "TICFAC_STATUS_PUSH"

// statusPushEnvDefault reads the `--status-push` flag's default: the
// operator's $TICFAC_STATUS_PUSH preference when it parses as a bool, off
// otherwise. Injected as a lookup so the tests read the production seam
// without owning the environment.
func statusPushEnvDefault(lookup func(string) string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(lookup(StatusPushEnv)))
	return err == nil && v
}

// statusSnapshotEnvelope is what one push carries: the model, plus the label
// map the page and the Telegram alerts name ticks by. The map is a rendering
// aid — the same gloss-or-title labels `tickLabels` reads — and it is neither
// a secret nor work product.
type statusSnapshotEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Host          string            `json:"host"`
	PushedAt      string            `json:"pushed_at"`
	Model         statusmodel.Model `json:"model"`
	TickLabels    map[string]string `json:"tick_labels,omitempty"`
}

// statusPushSetup decides WHERE a run pushes to: the configured factory's
// client, or none. The OPT-IN itself is the caller's `--status-push` flag — a
// per-run decision the command owns — so this seam answers only the question
// it can: is there a factory at all. A seam for tests: the production read
// is the operator's credential file, which a test replaces rather than writes.
var statusPushSetup = func() *cloudClient {
	client, err := newCloudClient()
	if err != nil {
		return nil
	}
	return client
}

// statusPusher is a started pusher: a loop goroutine pushing on the cadence,
// stopped by [statusPusher.Stop] which then pushes once more — the ending
// snapshot, taken when the run's terminal record exists.
type statusPusher struct {
	client *cloudClient
	repo   string
	runID  string
	warn   io.Writer
	stop   chan struct{}
	done   chan struct{}
	// stopOnce makes Stop idempotent: the defer in run-epic and a signal path
	// that already stopped the pusher must not double-close the channel.
	stopOnce sync.Once
}

// startStatusPusher starts the pusher for one run, or returns nil when the
// run did not opt in or no factory is configured — the shape `run-epic` is
// written against: a nil pusher is not an error, it is the default.
func startStatusPusher(repo, runID string, warn io.Writer, optedIn bool) *statusPusher {
	if !optedIn {
		return nil
	}
	client := statusPushSetup()
	if client == nil {
		return nil
	}
	p := &statusPusher{
		client: client,
		repo:   repo,
		runID:  runID,
		warn:   warn,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go p.loop()
	return p
}

func (p *statusPusher) loop() {
	defer close(p.done)
	p.push(context.Background())
	ticker := time.NewTicker(statusPushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.push(context.Background())
		}
	}
}

// Stop ends the cadence and pushes one final snapshot — the one that carries
// the run's own terminal answer, after the checkpoint has written it. A
// pusher started nil has no Stop; the caller checks. Idempotent: a second
// Stop neither hangs nor pushes again.
func (p *statusPusher) Stop() {
	p.stopOnce.Do(func() {
		close(p.stop)
		<-p.done
		p.push(context.Background())
	})
}

// push gathers and POSTs one snapshot. Best-effort by construction: every
// failure is a line to the log and nothing else.
func (p *statusPusher) push(ctx context.Context) {
	snapshot := statusSnapshotFor(ctx, p.repo, p.runID)
	if _, err := p.client.request(ctx, http.MethodPost, statusSnapshotPath, snapshot); err != nil {
		fmt.Fprintf(p.warn, "ticfac: the status snapshot for %s could not be pushed to the factory: %v\n", p.runID, err)
	}
}

// runlifeProbeOf is [runlife.Probe] as a seam, so a test can state liveness
// without building a run directory. Production always probes the real one.
var runlifeProbeOf = runlife.Probe

// statusSnapshotFor gathers the push's whole content: the same model
// `ticfac status --json` gathers, plus the label map. Separated from the push
// so a test pins the envelope's shape without a socket.
func statusSnapshotFor(ctx context.Context, repo, runID string) statusSnapshotEnvelope {
	model := localStatusModel(ctx, repo, runID, runlifeProbeOf(repo, runID, time.Now()), modelGatherers{graph: epicGraph, ci: statusCI})
	return statusSnapshotEnvelope{
		SchemaVersion: statusPushEnvelopeVersion,
		RunID:         model.RunID,
		Host:          statusmodel.HostLocal,
		PushedAt:      time.Now().UTC().Format(time.RFC3339),
		Model:         model,
		TickLabels:    tickLabels(ctx, repo),
	}
}
