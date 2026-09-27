package cli

// The local run's pusher (tick i1r): the half of the phone page that lives on
// the laptop. Three things are pinned here:
//
//   - the OPT-IN: nothing is pushed unless the operator asked (the
//     `--status-push` flag, whose default reads $TICFAC_STATUS_PUSH) AND a
//     factory is configured — read through the production seam, against a
//     real credential file;
//   - the WIRE SHAPE: the envelope the factory's snapshot door pins in
//     cloudflare/src/status.ts — envelope version, host "local", the model's
//     own `ticfac.status.v1` version, the run identity matching its model —
//     proved against a real HTTP server, not a fake client;
//   - the LIFECYCLE: one push when the run starts, one more when it stops —
//     the ending snapshot the page reads after the laptop closes — and a
//     push that fails is a line, never an exit.

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

func TestStatusPushFlagDefaultsFromTheConfiguredEnvironment(t *testing.T) {
	// The tick named the opt-in "a ticfac config flag": the flag's DEFAULT
	// reads the operator's set-once preference from $TICFAC_STATUS_PUSH — the
	// env vocabulary this repository already owns for operator preferences
	// ($TICFAC_RUNNER defaults --runner the same way), chosen because
	// ~/.ticfacrc's key vocabulary is the pinned credential-ownership bundle's
	// and closed. The flag on the command line always wins, both ways.
	define := func() *runEpicFlags {
		t.Helper()
		return defineRunEpicFlags(flag.NewFlagSet("run-epic", flag.ContinueOnError))
	}

	t.Setenv("TICFAC_STATUS_PUSH", "")
	if *define().statusPush {
		t.Error("an empty $TICFAC_STATUS_PUSH opted the run in")
	}
	t.Setenv("TICFAC_STATUS_PUSH", "1")
	if !*define().statusPush {
		t.Error("$TICFAC_STATUS_PUSH=1 did not opt the run in")
	}
	t.Setenv("TICFAC_STATUS_PUSH", "true")
	if !*define().statusPush {
		t.Error("$TICFAC_STATUS_PUSH=true did not opt the run in")
	}
	t.Setenv("TICFAC_STATUS_PUSH", "0")
	if *define().statusPush {
		t.Error("$TICFAC_STATUS_PUSH=0 opted the run in")
	}
	t.Setenv("TICFAC_STATUS_PUSH", "ture")
	if *define().statusPush {
		t.Error("a misspelled $TICFAC_STATUS_PUSH opted the run in: the value reads as a strict bool, a typo is off")
	}

	// The explicit flag overrides the config default, both ways.
	t.Setenv("TICFAC_STATUS_PUSH", "1")
	offFS := flag.NewFlagSet("run-epic", flag.ContinueOnError)
	offFlags := defineRunEpicFlags(offFS)
	if err := offFS.Parse([]string{"--status-push=false"}); err != nil {
		t.Fatalf("parse --status-push=false: %v", err)
	}
	if *offFlags.statusPush {
		t.Error("--status-push=false did not override a set $TICFAC_STATUS_PUSH")
	}
	t.Setenv("TICFAC_STATUS_PUSH", "0")
	onFS := flag.NewFlagSet("run-epic", flag.ContinueOnError)
	onFlags := defineRunEpicFlags(onFS)
	if err := onFS.Parse([]string{"--status-push=true"}); err != nil {
		t.Fatalf("parse --status-push=true: %v", err)
	}
	if !*onFlags.statusPush {
		t.Error("--status-push=true did not override an unset or off $TICFAC_STATUS_PUSH")
	}
}

// pushCapture is a factory's snapshot door as a test double: it records every
// request it takes and lets a test wait for the next one.
type pushCapture struct {
	t      *testing.T
	server *httptest.Server
	got    chan capturedPush
}

type capturedPush struct {
	method string
	path   string
	auth   string
	body   []byte
}

func newPushCapture(t *testing.T) *pushCapture {
	t.Helper()
	c := &pushCapture{t: t, got: make(chan capturedPush, 8)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 512<<10))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c.got <- capturedPush{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"stored":true}`))
	}))
	t.Cleanup(c.server.Close)
	return c
}

// next waits for the door's next request, failing the test on silence: a
// pusher that never pushes is the bug this waits to see.
func (c *pushCapture) next() capturedPush {
	c.t.Helper()
	select {
	case push := <-c.got:
		return push
	case <-time.After(10 * time.Second):
		c.t.Fatal("no snapshot push arrived: the pusher never pushed")
		return capturedPush{}
	}
}

// snapshotClient builds the cloud client the pusher pushes with, pointed at
// the capture: the same struct newCloudClient returns, without touching the
// operator's real credential file.
func snapshotClient(server *httptest.Server, token string) *cloudClient {
	return &cloudClient{baseURL: server.URL, token: token, http: server.Client()}
}

// overridePushSetup points the pusher's setup seam at one fixed client (or
// none) for the duration of a test.
func overridePushSetup(t *testing.T, client *cloudClient) {
	t.Helper()
	previous := statusPushSetup
	statusPushSetup = func() *cloudClient { return client }
	t.Cleanup(func() { statusPushSetup = previous })
}

// quietForge makes the gathering deterministic: no forge is asked, so no gh
// subprocess runs and no environment token can produce a CI answer.
func quietForge(t *testing.T) {
	t.Helper()
	previous := resolveForgeToken
	resolveForgeToken = func() (string, forge.TokenSource, error) {
		return "", "", os.ErrNotExist
	}
	t.Cleanup(func() { resolveForgeToken = previous })
	t.Setenv("GITHUB_TOKEN", "")
}

// writeCredential writes ~/.ticfacrc lines into a home, replacing the file.
func writeCredential(t *testing.T, home string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(home, ".ticfacrc"), []byte(body), 0o600); err != nil {
		t.Fatalf("write the credential file: %v", err)
	}
}

func TestStatusPushSetupReadsTheCredentialFile(t *testing.T) {
	quietForge(t)
	// The production read, through the real seam: a home whose ~/.ticfacrc
	// configures a factory answers with the client; anything less answers
	// none. The OPT-IN itself is the caller's --status-push flag — this seam
	// answers only "is there a factory at all".
	home := t.TempDir()
	empty := t.TempDir()
	t.Setenv("HOME", home)

	if client := statusPushSetup(); client != nil {
		t.Fatal("a home with no credential file produced a factory client")
	}
	writeCredential(t, home, "factory_url = https://factory.example.com", "factory_token = tkf_one")
	client := statusPushSetup()
	if client == nil {
		t.Fatal("a configured factory produced no client")
	}
	if client.baseURL != "https://factory.example.com" {
		t.Errorf("client baseURL = %q, want the configured factory", client.baseURL)
	}
	t.Setenv("HOME", empty)
	if client := statusPushSetup(); client != nil {
		t.Error("a home with no credential file produced a factory client")
	}
}

func TestStartStatusPusherIsNilWhenNotOptedIn(t *testing.T) {
	// The flag is off: no pusher even with a configured factory, because the
	// opt-in is the operator's, never the configuration's default.
	door := newPushCapture(t)
	overridePushSetup(t, snapshotClient(door.server, "tkf_testtoken"))
	if p := startStatusPusher(t.TempDir(), "epic-2jn", io.Discard, false); p != nil {
		p.Stop()
		t.Fatal("a run that did not opt in started a pusher")
	}
}

func TestStartStatusPusherIsNilWithoutAFactory(t *testing.T) {
	// The flag is on but no factory is configured: the opt-in is a no-op,
	// said by the nil return and nothing else.
	overridePushSetup(t, nil)
	if p := startStatusPusher(t.TempDir(), "epic-2jn", io.Discard, true); p != nil {
		p.Stop()
		t.Fatal("a run with no factory configured started a pusher")
	}
}

func TestPusherSendsTheEnvelopeTheFactoryDoorPins(t *testing.T) {
	quietForge(t)
	door := newPushCapture(t)
	overridePushSetup(t, snapshotClient(door.server, "tkf_testtoken"))

	pusher := startStatusPusher(t.TempDir(), "epic-2jn", io.Discard, true)
	if pusher == nil {
		t.Fatal("an opted-in run started no pusher")
	}

	push := door.next()
	if push.method != http.MethodPost {
		t.Errorf("push method = %q, want POST", push.method)
	}
	if push.path != statusSnapshotPath {
		t.Errorf("push path = %q, want %q", push.path, statusSnapshotPath)
	}
	if push.auth != "Bearer tkf_testtoken" {
		t.Errorf("push Authorization = %q, want the factory token", push.auth)
	}

	var envelope statusSnapshotEnvelope
	if err := json.Unmarshal(push.body, &envelope); err != nil {
		t.Fatalf("the pushed body is not the envelope: %v", err)
	}
	if envelope.SchemaVersion != statusPushEnvelopeVersion {
		t.Errorf("envelope schema_version = %d, want %d", envelope.SchemaVersion, statusPushEnvelopeVersion)
	}
	if envelope.Host != "local" {
		t.Errorf("envelope host = %q, want local", envelope.Host)
	}
	if envelope.RunID != "epic-2jn" {
		t.Errorf("envelope run_id = %q, want epic-2jn", envelope.RunID)
	}
	if envelope.Model.RunID != "epic-2jn" {
		t.Errorf("model run_id = %q, want epic-2jn (the door refuses a mismatch)", envelope.Model.RunID)
	}
	if envelope.Model.Host != "local" {
		t.Errorf("model host = %q, want local", envelope.Model.Host)
	}
	if envelope.Model.SchemaVersion == 0 {
		t.Error("the model carries no schema_version")
	}
	if _, err := time.Parse(time.RFC3339, envelope.PushedAt); err != nil {
		t.Errorf("pushed_at %q is not an RFC3339 stamp: %v", envelope.PushedAt, err)
	}

	// The ending push: Stop waits for the cadence to end and pushes once
	// more, after the run's own terminal record exists.
	pusher.Stop()
	if final := door.next(); final.method != http.MethodPost {
		t.Errorf("the ending push method = %q, want POST", final.method)
	}
}

func TestPusherStopIsIdempotentAndQuietAfterStop(t *testing.T) {
	quietForge(t)
	door := newPushCapture(t)
	overridePushSetup(t, snapshotClient(door.server, "tkf_testtoken"))

	pusher := startStatusPusher(t.TempDir(), "epic-2jn", io.Discard, true)
	if pusher == nil {
		t.Fatal("an opted-in run started no pusher")
	}
	door.next() // the starting push
	pusher.Stop()
	door.next() // the ending push

	// A second Stop must not hang and must not push again.
	pusher.Stop()
	select {
	case extra := <-door.got:
		t.Fatalf("a stopped pusher pushed again: %v", extra.method)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestStatusSnapshotForCarriesTheLabelMap(t *testing.T) {
	quietForge(t)
	previousLabels := tickLabels
	tickLabels = func(context.Context, string) map[string]string {
		return map[string]string{"i1r": "Follow ticfac from a phone"}
	}
	t.Cleanup(func() { tickLabels = previousLabels })

	envelope := statusSnapshotFor(context.Background(), t.TempDir(), "epic-2jn")
	if envelope.TickLabels["i1r"] != "Follow ticfac from a phone" {
		t.Errorf("envelope tick_labels = %v, want the label map the page names ticks by", envelope.TickLabels)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal the envelope: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("the envelope is not JSON: %v", err)
	}
	if _, ok := wire["tick_labels"]; !ok {
		t.Error("tick_labels did not reach the wire: the page cannot name ticks without it")
	}
	if _, ok := wire["model"]; !ok {
		t.Error("the model did not reach the wire")
	}
}

func TestPushFailureIsALineNeverAnExit(t *testing.T) {
	quietForge(t)
	// A factory that refuses everything: the pusher must say so and carry on.
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(refused.Close)
	overridePushSetup(t, snapshotClient(refused, "tkf_testtoken"))

	var warned strings.Builder
	pusher := startStatusPusher(t.TempDir(), "epic-2jn", &warned, true)
	if pusher == nil {
		t.Fatal("an opted-in run started no pusher")
	}
	// The starting push fails; the pusher logs and keeps running, and the
	// ending push is still attempted (and still only a line when refused).
	pusher.Stop()
	if !strings.Contains(warned.String(), "could not be pushed") {
		t.Errorf("a refused push said nothing: %q", warned.String())
	}
	// The line names the run, so an operator reading run.log can tell WHICH
	// run's remote view went dark.
	if !strings.Contains(warned.String(), "epic-2jn") {
		t.Errorf("the warning does not name the run: %q", warned.String())
	}
	if strings.Contains(warned.String(), "panic") {
		t.Error("a refused push panicked")
	}
}
