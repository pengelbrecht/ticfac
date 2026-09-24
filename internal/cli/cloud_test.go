package cli

// The command tests for `ticfac cloud`, ported from ticks' cmd/tk/cmd/
// cloud_test.go (and the cloud_tk_test.go / cloud_wave_test.go harnesses they
// lean on). ExecuteArgs + cobra's captured output become Run(args, stdout,
// stderr) and the exit code; the tk subprocess seam is stood in for with a
// generated fake tk script rather than ticks' self-exec trick, because this
// test binary does not ship the tracker.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

type cloudFactoryRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Auth   string
}

type cloudRoundTripper func(*http.Request) (*http.Response, error)

func (f cloudRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newCloudFactory(t *testing.T, handler func(cloudFactoryRequest) (int, any)) (string, *[]cloudFactoryRequest) {
	t.Helper()
	var mu sync.Mutex
	requests := make([]cloudFactoryRequest, 0)
	previousClient := cloudHTTPClient
	cloudHTTPClient = &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
		request := cloudFactoryRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Auth: r.Header.Get("Authorization"),
		}
		if r.Body != nil {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read factory request: %v", err)
			}
			if len(strings.TrimSpace(string(data))) > 0 {
				if err := json.Unmarshal(data, &request.Body); err != nil {
					t.Fatalf("decode factory request: %v", err)
				}
			}
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()

		status, body := handler(request)
		encoded := []byte{}
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				return nil, err
			}
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
			Request:    r,
		}, nil
	})}
	t.Cleanup(func() { cloudHTTPClient = previousClient })
	return "https://factory.test", &requests
}

func configureCloudFactory(t *testing.T, endpoint string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TICK_OWNER", "operator@example.com")

	config, err := credentials.LoadFrom(filepath.Join(home, credentials.FileName))
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	config.Set(credentials.KeyURL, endpoint)
	config.Set(credentials.KeyToken, "tkf_test-token")
	if err := config.Save(); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
}

// stubCloudTk stands a fake tk in front of the tracker reads: a shell script
// that answers `tk show <id> --json` from the fixtures the test has written
// under .tick/issues/ and `tk list --all --json` with all of them. The fake
// keeps the two outcomes cloudTkJSON holds apart: a missing tick refuses with
// tk's own exit 4, an answerable one prints the tick.
func stubCloudTk(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-tk.sh")
	script := `#!/bin/sh
case "$1" in
  show)
    id="$2"
    if [ -f ".tick/issues/$id.json" ]; then
      cat ".tick/issues/$id.json"
      exit 0
    fi
    echo "no tick $id in this checkout" >&2
    exit 4
    ;;
  list)
    echo '{"ticks":['
    first=1
    for f in .tick/issues/*.json; do
      [ -e "$f" ] || continue
      if [ "$first" = 1 ]; then first=0; else echo ","; fi
      cat "$f"
    done
    echo ']}'
    exit 0
    ;;
  *)
    echo "unsupported by the fake tk: $*" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tk: %v", err)
	}
	inner := cloudTkBinary
	cloudTkBinary = func() (string, []string, error) { return bin, nil, nil }
	t.Cleanup(func() { cloudTkBinary = inner })
}

// setupCloudRepo makes a clean checkout with a bare origin it pushes to, the
// way the cloud tests in ticks did, and leaves the process cwd inside it —
// which is how the cloud commands (and the fake tk) find it.
func setupCloudRepo(t *testing.T, withEpic bool) (repo, remote, sha string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "checkout")
	remote = filepath.Join(root, "acme", "project.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("mkdir remote parent: %v", err)
	}
	execTestCmd(t, root, "git", "init", "--bare", remote)
	execTestCmd(t, root, "git", "init", repo)
	execTestCmd(t, repo, "git", "checkout", "-b", "main")
	execTestCmd(t, repo, "git", "config", "user.email", "operator@example.com")
	execTestCmd(t, repo, "git", "config", "user.name", "Operator")
	execTestCmd(t, repo, "git", "remote", "add", "origin", "file://"+remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("cloud test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	execTestCmd(t, repo, "git", "add", "README.md")
	execTestCmd(t, repo, "git", "commit", "-m", "base")
	execTestCmd(t, repo, "git", "push", "-u", "origin", "main")

	if withEpic {
		writeCloudEpic(t, repo, "epic1")
		execTestCmd(t, repo, "git", "add", ".tick")
		execTestCmd(t, repo, "git", "commit", "-m", "add epic")
		execTestCmd(t, repo, "git", "push", "origin", "main")
	}

	sha = strings.TrimSpace(string(execTestOutput(t, repo, "git", "rev-parse", "HEAD")))
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir to cloud repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	return repo, remote, sha
}

// setupCloudWaveRepo is setupCloudRepo with an epic and a set of child ticks
// committed and pushed, for the --tick-ids wave checks.
func setupCloudWaveRepo(t *testing.T, ticks ...string) (repo, remote, sha string) {
	t.Helper()
	repo, remote, _ = setupCloudRepo(t, false)
	writeCloudEpic(t, repo, "epic1")
	for _, id := range ticks {
		writeCloudTick(t, repo, id, "epic1")
	}
	execTestCmd(t, repo, "git", "add", "-A")
	execTestCmd(t, repo, "git", "commit", "-m", "epic and wave")
	execTestCmd(t, repo, "git", "push", "origin", "main")
	sha = strings.TrimSpace(string(execTestOutput(t, repo, "git", "rev-parse", "HEAD")))
	return repo, remote, sha
}

func writeCloudEpic(t *testing.T, repo, id string) {
	t.Helper()
	writeCloudTickFixture(t, repo, cloudTickFixture{
		ID: id, Title: "Cloud test epic", Type: "epic",
		Owner: "operator", CreatedBy: "operator",
	})
}

// cloudTickFixture is the handful of fields a cloud test needs to seed a
// tick on disk. It exists so writeCloudTickFixture stays a plain literal at
// every call site instead of a long positional argument list.
type cloudTickFixture struct {
	ID        string
	Title     string
	Type      string
	Parent    string
	Owner     string
	CreatedBy string
}

// writeCloudTickFixture writes a tick fixture directly as the JSON document
// cloudReadTracker parses back out via `tk show`/`tk list --all` (cloud.go).
//
// It is a copy of what the tracker's Store.Write does for these fields, not an
// import of it: the cloud commands treat the tracker as a JSON contract for
// READS, and a test file in this package follows the same rule for writes, so
// that neither side of a cloud-command test depends on the tracker's internal
// Go types (5yk). Keep the field set in sync by hand with the wire shape if
// cloudReadTracker ever starts reading more of them.
func writeCloudTickFixture(t *testing.T, repo string, f cloudTickFixture) {
	t.Helper()
	if f.ID == "" {
		t.Fatalf("tick fixture is missing an id")
	}
	now := time.Now().UTC().Truncate(time.Second)
	doc := map[string]any{
		"id": f.ID, "title": f.Title, "status": "open", "priority": 2,
		"type": f.Type, "owner": f.Owner, "created_by": f.CreatedBy,
		"created_at": now, "updated_at": now,
	}
	if f.Parent != "" {
		doc["parent"] = f.Parent
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode tick fixture %s: %v", f.ID, err)
	}
	dir := filepath.Join(repo, ".tick", "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .tick/issues: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.ID+".json"), data, 0o644); err != nil {
		t.Fatalf("write tick fixture %s: %v", f.ID, err)
	}
}

func writeCloudTick(t *testing.T, repo, id, epic string) {
	t.Helper()
	writeCloudTickFixture(t, repo, cloudTickFixture{
		ID: id, Title: "Tick " + id, Type: "task",
		Parent: epic, Owner: "operator", CreatedBy: "operator",
	})
}

func execTestOutput(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v: %v\n%s", name, args, err, out)
	}
	return out
}

func execTestCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v: %v\n%s", name, args, err, out)
	}
}

// runCloudArgs runs one command through the surface under test, so a ported
// test reads like its ticks original: one call, stdout and stderr apart, and
// the exit code back.
func runCloudArgs(t *testing.T, args []string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, &out, &errOut
}

func TestCloudRunRejectsAnUnpushedEpicBeforeSubmitting(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, false)
	writeCloudEpic(t, repo, "epic1")

	var calls int
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		calls++
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_should_not_start"}}
	})
	configureCloudFactory(t, endpoint)

	code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"})
	if code == exitSuccess {
		t.Fatal("cloud run accepted an epic whose tick file was not pushed")
	}
	if !strings.Contains(stderr.String(), "epic1") || !strings.Contains(strings.ToLower(stderr.String()), "push") {
		t.Fatalf("error does not explain how to publish the epic: %s", stderr.String())
	}
	if calls != 0 {
		t.Fatalf("factory received %d submission(s) after the local push check failed", calls)
	}
}

func TestCloudRunPushesAndSubmitsThePinnedCommit(t *testing.T) {
	stubCloudTk(t)
	repo, _, baseSHA := setupCloudRepo(t, true)
	execTestCmd(t, repo, "git", "commit", "--allow-empty", "-m", "local change")
	localSHA := strings.TrimSpace(string(execTestOutput(t, repo, "git", "rev-parse", "HEAD")))

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method != http.MethodPost || request.Path != "/api/runs" {
			return http.StatusNotFound, map[string]any{"error": "not_found"}
		}
		return http.StatusCreated, map[string]any{
			"run":      map[string]any{"run_id": "run_started", "state": "starting"},
			"workflow": map[string]any{"id": "run_started", "status": "running"},
		}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1", "--notify", "telegram"})
	if code != exitSuccess {
		t.Fatalf("cloud run: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "run_started") {
		t.Fatalf("cloud run did not print the run id:\n%s", out.String())
	}
	if len(*requests) != 1 {
		t.Fatalf("factory received %d request(s), want one", len(*requests))
	}
	request := (*requests)[0]
	if request.Auth != "Bearer tkf_test-token" {
		t.Errorf("authorization = %q", request.Auth)
	}
	for field, want := range map[string]any{
		"project":      "acme/project",
		"epic":         "epic1",
		"base_sha":     localSHA,
		"requested_by": "operator@example.com",
		"notify":       "telegram",
	} {
		if got := request.Body[field]; got != want {
			t.Errorf("request %s = %#v, want %#v", field, got, want)
		}
	}
	remoteSHA := strings.Fields(string(execTestOutput(t, repo, "git", "ls-remote", "origin", "refs/heads/main")))[0]
	if remoteSHA != localSHA {
		t.Errorf("remote branch = %s, want pushed HEAD %s (base was %s)", remoteSHA, localSHA, baseSHA)
	}
}

func TestCloudRunNamesLeaseHolderAndQueueIsOptIn(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	var submissions int
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Path != "/api/runs" {
			return http.StatusNotFound, map[string]any{"error": "not_found"}
		}
		submissions++
		switch submissions {
		case 1:
			return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_holder", "state": "starting"}}
		case 2:
			return http.StatusConflict, map[string]any{
				"error":  "lease_held",
				"detail": "project is already running",
				"holder": map[string]any{"run_id": "run_holder", "epic": "epic1"},
			}
		default:
			return http.StatusAccepted, map[string]any{
				"queued": map[string]any{"run_id": "run_queued", "blocked_by": "run_holder"},
				"holder": map[string]any{"run_id": "run_holder"},
			}
		}
	})
	configureCloudFactory(t, endpoint)

	if code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"}); code != exitSuccess {
		t.Fatalf("first cloud run: %s", stderr.String())
	}
	code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"})
	if code == exitSuccess || !strings.Contains(stderr.String(), "run_holder") {
		t.Fatalf("lease refusal = %s, want holder run id", stderr.String())
	}
	code, out, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1", "--queue"})
	if code != exitSuccess {
		t.Fatalf("queued cloud run: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "run_queued") || !strings.Contains(out.String(), "run_holder") {
		t.Fatalf("queue output does not identify the parked run and holder:\n%s", out.String())
	}
	if got := (*requests)[1].Body["queue"]; got != false {
		t.Errorf("refused submission queue = %#v, want false", got)
	}
	if got := (*requests)[2].Body["queue"]; got != true {
		t.Errorf("queued submission queue = %#v, want true", got)
	}
}

func TestCloudStopAndStatusUseTheFactoryRunSurface(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodPost && request.Path == "/api/runs":
			return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_live", "state": "starting"}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/run_live/stop":
			return http.StatusOK, map[string]any{"run": map[string]any{"run_id": "run_live", "state": "stopping"}}
		case request.Method == http.MethodGet && request.Path == "/api/runs":
			return http.StatusOK, map[string]any{
				"runs":     []any{map[string]any{"run_id": "run_live", "state": "stopping", "epic": "epic1"}},
				"projects": []any{map[string]any{"project": "acme/project", "lease": map[string]any{"run_id": "run_live"}}},
			}
		default:
			return http.StatusNotFound, map[string]any{"error": "not_found"}
		}
	})
	configureCloudFactory(t, endpoint)

	if code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"}); code != exitSuccess {
		t.Fatalf("cloud run: %s", stderr.String())
	}
	code, out, stderr := runCloudArgs(t, []string{"cloud", "stop", "run_live"})
	if code != exitSuccess {
		t.Fatalf("cloud stop: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "run_live") || !strings.Contains(out.String(), "stopping") {
		t.Fatalf("stop output lacks run and state:\n%s", out.String())
	}
	code, out, stderr = runCloudArgs(t, []string{"cloud", "status"})
	if code != exitSuccess {
		t.Fatalf("cloud status: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "run_live") || !strings.Contains(out.String(), "stopping") {
		t.Fatalf("status output lacks live run:\n%s", out.String())
	}
	if len(*requests) != 3 || (*requests)[1].Path != "/api/runs/run_live/stop" || (*requests)[2].Path != "/api/runs" {
		t.Fatalf("factory requests = %#v, want run, stop, status", *requests)
	}
	if (*requests)[1].Auth != "Bearer tkf_test-token" || (*requests)[2].Auth != "Bearer tkf_test-token" {
		t.Errorf("stop/status did not use the factory bearer token")
	}
}

func TestCloudWithoutFactoryConfigurationNamesSetup(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, false)
	t.Setenv("HOME", t.TempDir())

	code, _, stderr := runCloudArgs(t, []string{"cloud", "status"})
	if code == exitSuccess {
		t.Fatal("cloud status succeeded without a factory")
	}
	if !strings.Contains(stderr.String(), "tk factory setup") {
		t.Fatalf("missing-factory error does not name setup: %s", stderr.String())
	}
}

// D21 fixes the operator-to-orchestrator COMMAND vocabulary at run, stop,
// status and answer. In ticks the `cloud` family also carries the D19 dispatch
// verbs (spawn, wait, collect, reconcile) for a LOCAL orchestrator driving
// cloud workers; ticfac's reconciler owns that role, so this surface carries
// only the steering and observation verbs. The split is pinned here rather
// than left implicit, because a reader counting subcommands would otherwise
// conclude D21 had been violated — or that the dispatch verbs had been lost by
// accident.
func TestCloudExposesOnlyTheClosedCommandVocabulary(t *testing.T) {
	steering := map[string]bool{"run": true, "stop": true}
	// `supervisor` reads the run's Workflow instance from OUTSIDE the factory
	// (tick acy). It observes and cannot steer, so it is an observation like
	// the other three — the credential it uses is the operator's own read-only
	// Cloudflare token, not a door into the run.
	observation := map[string]bool{"status": true, "logs": true, "trace": true, "supervisor": true}

	dispatched := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(cloudUsage), "\n") {
		line = strings.TrimSpace(line)
		// The usage header ("ticfac cloud — ...") is not a subcommand.
		if !strings.HasPrefix(line, "ticfac cloud ") || strings.HasPrefix(line, "ticfac cloud —") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			dispatched[fields[2]] = true
		}
	}
	for name := range dispatched {
		if !steering[name] && !observation[name] {
			t.Errorf("unexpected cloud command %q: it is neither a D21 verb nor a read-only observation", name)
		}
	}
	if len(dispatched) != len(steering)+len(observation) {
		t.Fatalf("cloud commands = %d (%v), want exactly the %d D21 and observation verbs",
			len(dispatched), dispatched, len(steering)+len(observation))
	}
	if !strings.Contains(cloudUsage, "D21") {
		t.Errorf("the cloud help does not say why a non-steering command does not widen D21's vocabulary")
	}
}

func TestCloudStatusNamesTheImageTheRunBooted(t *testing.T) {
	setupCloudRepo(t, true)
	digest := "sha256:" + strings.Repeat("b", 64)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method == http.MethodGet && request.Path == "/api/runs/run_live" {
			return http.StatusOK, map[string]any{
				"run":   map[string]any{"run_id": "run_live", "state": "running", "epic": "epic1"},
				"image": map[string]any{"image_ref": "registry.example.com/acct/ticks-orchestrator@" + digest, "image_digest": digest},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_live"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_live: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), digest) {
		t.Errorf("status does not name the image the run booted:\n%s", out.String())
	}
}

// A run that exited 0 having changed nothing is not a completion (tick ehy).
// The state carries the distinction and status has to make it legible, or an
// operator resubmits an epic believing the last run advanced it.
func TestCloudStatusDistinguishesAStoppedNoOpFromACompletion(t *testing.T) {
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_noop":
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": "run_noop", "state": "stopped", "epic": "epic1"},
				"progress": map[string]any{
					"progress": "none",
					"detail":   "no branch on origin changed while the run was alive",
				},
			}
		case "/api/runs/run_real":
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": "run_real", "state": "completed", "epic": "epic1"},
				"progress": map[string]any{
					"progress": "advanced",
					"detail":   "1 branch on origin changed while the run was alive (epic/epic1)",
				},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_noop"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_noop: %s\n%s", stderr.String(), out.String())
	}
	noop := out.String()
	if !strings.Contains(noop, "state: stopped") {
		t.Errorf("a run that did nothing is not reported as stopped:\n%s", noop)
	}
	if !strings.Contains(noop, "progress: none") {
		t.Errorf("status does not say the run changed nothing:\n%s", noop)
	}
	if !strings.Contains(noop, "no branch on origin changed") {
		t.Errorf("status does not say why the run changed nothing:\n%s", noop)
	}

	code, out, stderr = runCloudArgs(t, []string{"cloud", "status", "run_real"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_real: %s\n%s", stderr.String(), out.String())
	}
	real := out.String()
	if !strings.Contains(real, "state: completed") || !strings.Contains(real, "progress: advanced") {
		t.Errorf("a run that advanced the epic is not reported as such:\n%s", real)
	}
}

// A run finalized before durable-evidence finalization existed has no verdict.
// Silence would read as "advanced", which is the assumption this whole change
// exists to remove.
func TestCloudStatusSaysWhenNoProgressVerdictWasRecorded(t *testing.T) {
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Path == "/api/runs/run_legacy" {
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": "run_legacy", "state": "completed", "epic": "epic1"},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_legacy"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_legacy: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "progress: unrecorded") {
		t.Errorf("status is silent about the missing progress verdict:\n%s", out.String())
	}
}

// A live run has no verdict yet, and saying "unrecorded" about one still
// working would be noise rather than a fact.
func TestCloudStatusIsSilentAboutProgressForALiveRun(t *testing.T) {
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Path == "/api/runs/run_live" {
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": "run_live", "state": "running", "epic": "epic1"},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_live"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_live: %s\n%s", stderr.String(), out.String())
	}
	if strings.Contains(out.String(), "progress:") {
		t.Errorf("status reports a progress verdict for a run still working:\n%s", out.String())
	}
}

// A run with no recorded image says so. Omitting the line would read as "the
// same image as every other run", which is the assumption that cost the
// diagnose-fix-deploy cycles this exists to prevent.
func TestCloudStatusSaysWhenNoImageWasRecorded(t *testing.T) {
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method == http.MethodGet && request.Path == "/api/runs/run_old" {
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": "run_old", "state": "completed", "epic": "epic1"},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_old"})
	if code != exitSuccess {
		t.Fatalf("cloud status run_old: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "image: unrecorded") {
		t.Errorf("status is silent about the missing image:\n%s", out.String())
	}
}

// TestCloudStopNowAsksForAHardStopAndSaysWhichItPerformed pins tick gyl's
// operator-facing half: `--now` must reach the factory as a hard stop, and the
// output must say which stop happened rather than leaving an operator to
// assume the one they asked for is the one they got.
func TestCloudStopNowAsksForAHardStopAndSaysWhichItPerformed(t *testing.T) {
	setupCloudRepo(t, true)
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method == http.MethodPost && request.Path == "/api/runs/run_live/stop" {
			mode, _ := request.Body["mode"].(string)
			return http.StatusOK, map[string]any{
				"run":            map[string]any{"run_id": "run_live", "state": "stopping"},
				"mode":           mode,
				"tokens_revoked": 1,
			}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "stop", "run_live", "--now"})
	if code != exitSuccess {
		t.Fatalf("cloud stop --now: %s\n%s", stderr.String(), out.String())
	}
	if got := (*requests)[0].Body["mode"]; got != "hard" {
		t.Fatalf("stop mode = %#v, want hard", got)
	}
	output := out.String()
	if !strings.Contains(output, "hard stop") {
		t.Fatalf("--now output does not say a hard stop was performed:\n%s", output)
	}
	if !strings.Contains(output, "gateway credentials revoked: 1") {
		t.Fatalf("--now output does not report the revocation:\n%s", output)
	}

	// The default is still the clean stop, and says so — including how to ask
	// for the other one.
	code, out, stderr = runCloudArgs(t, []string{"cloud", "stop", "run_live"})
	if code != exitSuccess {
		t.Fatalf("cloud stop: %s\n%s", stderr.String(), out.String())
	}
	if got := (*requests)[1].Body["mode"]; got != "clean" {
		t.Fatalf("default stop mode = %#v, want clean", got)
	}
	clean := out.String()
	if !strings.Contains(clean, "clean stop") || !strings.Contains(clean, "--now") {
		t.Fatalf("clean stop output does not name the hard variant:\n%s", clean)
	}
}

// A budget is a per-invocation choice, not a redeploy. The flags ride the
// submit payload; the deployment ceiling still bounds them on the far side.
func TestCloudRunCarriesPerRunBudgetOverridesInTheSubmission(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method != http.MethodPost || request.Path != "/api/runs" {
			return http.StatusNotFound, map[string]any{"error": "not_found"}
		}
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_bounded"}}
	})
	configureCloudFactory(t, endpoint)

	code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1", "--max-cost", "2.50", "--max-wall-clock", "45m"})
	if code != exitSuccess {
		t.Fatalf("bounded cloud run: %s", stderr.String())
	}
	body := (*requests)[0].Body
	if got := body["max_cost_usd"]; got != 2.5 {
		t.Errorf("max_cost_usd = %#v, want 2.5", got)
	}
	if got := body["max_wall_clock_ms"]; got != float64(2_700_000) {
		t.Errorf("max_wall_clock_ms = %#v, want 2700000", got)
	}

	// Without the flags the submission carries no budget at all, so the
	// deployment's own ceiling stands rather than being restated by the CLI.
	code, _, stderr = runCloudArgs(t, []string{"cloud", "run", "epic1"})
	if code != exitSuccess {
		t.Fatalf("unbounded cloud run: %s", stderr.String())
	}
	plain := (*requests)[1].Body
	if _, ok := plain["max_cost_usd"]; ok {
		t.Errorf("submission carries max_cost_usd with no flag set: %#v", plain["max_cost_usd"])
	}
	if _, ok := plain["max_wall_clock_ms"]; ok {
		t.Errorf("submission carries max_wall_clock_ms with no flag set: %#v", plain["max_wall_clock_ms"])
	}
}

// tick 7zk. The operator asked for --max-cost 40 and got $8, because the
// deployment's RUN_MAX_COST_USD was 8 and a submission may only lower a budget.
// The policy is right; the silence was not. The number that will actually
// govern has to appear at the moment the flag is typed, not in the cancellation
// that ends the run — the third time in one epic a deployment ceiling replaced
// an operator's number with no line anywhere saying so.
func TestCloudRunPrintsTheEffectiveBudgetItWillActuallyRunUnder(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method != http.MethodPost || request.Path != "/api/runs" {
			return http.StatusNotFound, map[string]any{"error": "not_found"}
		}
		return http.StatusCreated, map[string]any{
			"run": map[string]any{"run_id": "run_clamped", "state": "starting"},
			"budget": map[string]any{
				"max_cost_usd":                8,
				"max_wall_clock_ms":           14_400_000,
				"requested_max_cost_usd":      40,
				"requested_max_wall_clock_ms": nil,
				"cost_clamped":                true,
				"wall_clock_clamped":          false,
			},
		}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1", "--max-cost", "40"})
	if code != exitSuccess {
		t.Fatalf("cloud run: %s", stderr.String())
	}
	printed := out.String()
	// The number that governs.
	if !strings.Contains(printed, "cost budget: $8.00") {
		t.Errorf("the effective cost budget is not printed:\n%s", printed)
	}
	if !strings.Contains(printed, "wall-clock budget: 4h0m0s") {
		t.Errorf("the effective wall-clock budget is not printed:\n%s", printed)
	}
	// And that it is not the number that was asked for.
	if !strings.Contains(printed, "--max-cost $40.00 was lowered") {
		t.Errorf("the clamp is not reported:\n%s", printed)
	}
}

// A factory deployed before the budget was reported answers without it. Saying
// "$0.00" there would be worse than saying nothing.
func TestCloudRunSaysNothingAboutABudgetTheFactoryDidNotReport(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_old"}}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1"})
	if code != exitSuccess {
		t.Fatalf("cloud run: %s", stderr.String())
	}
	if strings.Contains(out.String(), "budget") {
		t.Errorf("a budget line was invented for a factory that reported none:\n%s", out.String())
	}
}

func TestCloudRunRefusesANonPositiveBudgetBeforeSubmitting(t *testing.T) {
	stubCloudTk(t)
	setupCloudRepo(t, true)
	var calls int
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		calls++
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_should_not_start"}}
	})
	configureCloudFactory(t, endpoint)

	for _, args := range [][]string{
		{"cloud", "run", "epic1", "--max-cost", "0"},
		{"cloud", "run", "epic1", "--max-wall-clock", "-5m"},
	} {
		code, _, stderr := runCloudArgs(t, args)
		if code == exitSuccess {
			t.Fatalf("%v was accepted, want a refusal", args)
		}
		if !strings.Contains(stderr.String(), "positive") {
			t.Errorf("%v error does not say the budget must be positive: %s", args, stderr.String())
		}
	}
	if calls != 0 {
		t.Fatalf("factory received %d submission(s) after an invalid budget", calls)
	}
}

// --tick-ids is gone (tick l6t): the Run Workflow no longer fans ticks out
// to worker containers itself, so the flag that requested a wave must be a
// plain unknown-flag refusal — not a silent no-op, and not a submission the
// factory would have to interpret.
func TestCloudRunRefusesTheDeletedTickIDsFlag(t *testing.T) {
	stubCloudTk(t)
	setupCloudWaveRepo(t, "aaa")
	var calls int
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		calls++
		return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": "run_should_not_start"}}
	})
	configureCloudFactory(t, endpoint)

	code, _, stderr := runCloudArgs(t, []string{"cloud", "run", "epic1", "--tick-ids", "aaa"})
	if code == exitSuccess {
		t.Fatal("cloud run accepted --tick-ids, a flag the wave path it named is deleted")
	}
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d (an unknown flag is a usage error)", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "tick-ids") {
		t.Errorf("refusal %q does not name the flag the operator typed", stderr.String())
	}
	if calls != 0 {
		t.Fatalf("factory received %d submission(s) for a flag it no longer accepts", calls)
	}
}
