package cli

// The status pusher (ticfac tick i1r, and h7w): the half of "follow ticfac
// from a phone" that lives where the run works, pushing the one model every
// surface renders to the factory the phone page reads.
//
// TWO pushers, one struct, deliberately:
//
//   - A LOCAL run is invisible to the factory by construction — its
//     tracker, its .ticfac/ records and its event feed are on the laptop that
//     runs it — so the run reports ITSELF: [startStatusPusher] gathers the
//     same status model `ticfac status --json` gathers and POSTs it as a
//     small snapshot to the operator's snapshot door (`POST
//     /api/status-snapshots`), on a short cadence while the run works and
//     once more at each ending, when the terminal record exists. OPT-IN:
//     the `--status-push` flag on `run-epic`, whose default reads the
//     operator's set-once preference from $TICFAC_STATUS_PUSH — and a
//     factory is configured (the operator's ~/.ticfacrc). The flag is a
//     flag and its config is the $TICFAC_* environment — the vocabulary
//     this repository owns for operator preferences — because ~/.ticfacrc's
//     key vocabulary is the pinned credential-ownership bundle's and closed
//     ("an unknown factory_ key is a typo"), and the command-line flag
//     always wins over the environment, both ways. No opt-in, or no
//     factory to push to, and the pusher is nil and costs nothing.
//
//   - A CLOUD run's factory composes a status document from its own records
//     (cloudflare/src/status.ts cloudStatusDoc), and that composition is
//     honest about what the factory can state — which is not the model's
//     whole half: no health verdict, no tick table, no per-source cost the
//     Go model builds from the records the container reads. So the
//     container's own `run-epic` pushes the model it gathers IN SITU — the
//     same one `ticfac watch run_<hex>` builds for the same run — to the
//     factory's run-credential door (`POST /api/status-relay`), so the phone
//     renders the SAME model the terminal does (hn6 A5, rule 8: two
//     renderers, they cannot disagree). NOT OPT-IN, exactly like the feed
//     relay it sits beside (internal/feedrelay): the run is already the
//     factory's — its records, its feed and its branches live there — and a
//     cloud run has no phone view worth the name without it. The detection
//     is the relay's own: TICKS_FACTORY_URL, TICKS_FACTORY_TOKEN and a
//     TICKS_RUN_ID that names THIS run, the booted orchestrator's own
//     environment and nobody else's.
//
// Three rules shape both, inherited from the local half the cloud one grew
// out of:
//
//   - BEST-EFFORT, ALWAYS. The pusher can never slow, stop or fail the run
//     it reports: a push that fails is a line in run.log, and the next
//     cadence tries again. The remote view is a convenience; the run is the
//     work.
//
//   - NO SECRETS, NO WORK PRODUCT. The snapshot carries the status model and
//     the label map the page names ticks by (tick q90) — nothing else. The
//     model is the same public answer `ticfac status --json` prints; worker
//     code, reports and credentials never ride it.
//
//   - HONEST HOSTS. The envelope's host is the run's own — "local" for a
//     laptop run, "cloud" for one the factory hosts — and the model's host
//     agrees, because the clearing commands are spelled by it (a cloud
//     run's resume is a new submission to its factory, never a local
//     `run-epic` on whatever machine reads the page).
//
// A local run pauses when the laptop sleeps; the page says PAUSED/STALE when
// the snapshots stop, which is exactly the honest reading — the pusher does
// not try to be cleverer than its own silence. A cloud run's container that
// dies without its terminal push leaves its run row as the liveness
// authority, and the page reads that row before it believes a stale alive
// model.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// statusPushInterval is the push cadence: short enough that the page's
// staleness window (90s) means something, long enough that a run pushing every
// 30 seconds is one small write, not a conversation.
const statusPushInterval = 30 * time.Second

// statusSnapshotPath is the OPERATOR's snapshot door, the one a local run's
// pusher talks to. The TS half pins the contract (cloudflare/src/status.ts):
// envelope version, the model's own `ticfac.status.v1` version, a local
// host, and the run identity matching its model.
const statusSnapshotPath = "/api/status-snapshots"

// statusRelayPath is the RUN-CREDENTIAL snapshot door, the one a cloud run's
// orchestrator container talks to (hn6 h7w): the same envelope, authorized
// by the run's own gateway token rather than the operator's — a container
// holds its run's credential, never the operator's (the same rule the feed
// relay's door holds).
const statusRelayPath = "/api/status-relay"

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
	// path is the door this pusher talks to: the operator's snapshot door
	// for a local run, the run-credential relay for a cloud one.
	path string
	// host is the run's own host, the envelope's and the model's both: the
	// door refuses a model that disagrees with its envelope, and the page
	// reads the host to spell the run's clearing commands.
	host  string
	repo  string
	runID string
	warn  io.Writer
	stop  chan struct{}
	done  chan struct{}
	// stopOnce makes Stop idempotent: the defer in run-epic and a signal path
	// that already stopped the pusher must not double-close the channel.
	stopOnce sync.Once
}

// startStatusPusher starts the LOCAL run's pusher, or returns nil when the
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
	return newStatusPusher(client, statusSnapshotPath, statusmodel.HostLocal, repo, runID, warn)
}

// The orchestrator container's environment, the same three names the feed
// relay reads (internal/feedrelay): the factory, the run's own gateway
// credential, and the run this process was booted to be. The id is what
// keeps a laptop that merely has a factory configured from pushing anything —
// only a process whose run IS the booted run pushes.
const (
	cloudPushFactoryURLEnv   = "TICKS_FACTORY_URL"
	cloudPushFactoryTokenEnv = "TICKS_FACTORY_TOKEN"
	cloudPushRunIDEnv        = "TICKS_RUN_ID"
)

// cloudPushClientFromEnv is the cloud run's pusher's WHERE: the run's own
// factory and its own run-scoped credential, read from the container's
// environment — or nil when this process is not a cloud run's booted
// orchestrator. The same gate [feedrelay.FromEnv] holds, for the same
// reason: those three variables together are the orchestrator container's
// own boot, and nobody else's.
var cloudPushClientFromEnv = func(runID string) *cloudClient {
	factory := strings.TrimSpace(os.Getenv(cloudPushFactoryURLEnv))
	token := strings.TrimSpace(os.Getenv(cloudPushFactoryTokenEnv))
	booted := strings.TrimSpace(os.Getenv(cloudPushRunIDEnv))
	if factory == "" || token == "" || booted == "" || booted != runID {
		return nil
	}
	parsed, err := url.Parse(factory)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil
	}
	if cloudHTTPClient == nil {
		cloudHTTPClient = httpnet.Client(15 * time.Second)
	}
	return &cloudClient{baseURL: strings.TrimRight(factory, "/"), token: token, http: cloudHTTPClient}
}

// startCloudStatusPusher starts the CLOUD run's pusher — the model its own
// orchestrator container gathers in situ, on the run-credential door — or
// returns nil outside an orchestrator container. Not opt-in, exactly like
// the feed relay: a cloud run belongs to its factory already, and the phone
// page's cloud row without the run's own model is the bare state chip this
// tick exists to replace. A seam for tests: the production read is the
// container's environment, which a test replaces rather than owns.
func startCloudStatusPusher(repo, runID string, warn io.Writer) *statusPusher {
	client := cloudPushClientFromEnv(runID)
	if client == nil {
		return nil
	}
	return newStatusPusher(client, statusRelayPath, statusmodel.HostCloud, repo, runID, warn)
}

// newStatusPusher builds and starts one pusher against a door. The loop is
// the same for both hosts; only the door, the credential and the envelope's
// host differ.
func newStatusPusher(client *cloudClient, path, host, repo, runID string, warn io.Writer) *statusPusher {
	p := &statusPusher{
		client: client,
		path:   path,
		host:   host,
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
	snapshot := statusSnapshotFor(ctx, p.repo, p.runID, p.host)
	if _, err := p.client.request(ctx, http.MethodPost, p.path, snapshot); err != nil {
		fmt.Fprintf(p.warn, "ticfac: the status snapshot for %s could not be pushed to the factory: %v\n", p.runID, err)
	}
}

// runlifeProbeOf is [runlife.Probe] as a seam, so a test can state liveness
// without building a run directory. Production always probes the real one.
var runlifeProbeOf = runlife.Probe

// statusSnapshotFor gathers the push's whole content: the same model
// `ticfac status --json` gathers, plus the label map. Separated from the push
// so a test pins the envelope's shape without a socket. The host is the
// caller's — a local run's own laptop model, a cloud run's in-situ model —
// and it rides both the envelope and the model, because the clearing
// commands the model spells are the host's (a cloud run's resume is a new
// submission to its factory).
func statusSnapshotFor(ctx context.Context, repo, runID, host string) statusSnapshotEnvelope {
	if host == "" {
		host = statusmodel.HostLocal
	}
	model := localStatusModelHosted(ctx, repo, runID, runlifeProbeOf(repo, runID, time.Now()), modelGatherers{graph: epicGraph, ci: statusCI}, host)
	return statusSnapshotEnvelope{
		SchemaVersion: statusPushEnvelopeVersion,
		RunID:         model.RunID,
		Host:          host,
		PushedAt:      time.Now().UTC().Format(time.RFC3339),
		Model:         model,
		TickLabels:    tickLabels(ctx, repo),
	}
}
