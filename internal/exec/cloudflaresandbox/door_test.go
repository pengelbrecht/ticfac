package cloudflaresandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The door, faked: an httptest server that answers the two routes
// cloudflare/src/sandbox-dispatch.ts serves — POST /api/sandbox/attempts and
// GET /api/sandbox/attempts/:tick_id/:attempt — with the shapes that file
// documents, and records what it was asked so a test can assert the contract
// rather than the implementation.
//
// The fake models the door's OWN rules, not this client's, so a client bug
// that quietly changes the request is caught by the fake refusing it:
//   - both routes answer BY IDENTITY — the FULL job id (the start body's
//     job_id, the status route's ?job_id=), because several jobs run under
//     one attempt number and each is its own container; a request that
//     names no job is the attempt's own job, derived from the CREDENTIAL's
//     run the way the real route derives it; a job id naming another run is
//     refused, as the real route refuses it;
//   - an identity the door was never told about answers `lost`, the observer's
//     statement, not a verdict;
//   - a start is answered with a handle, never with a result — the fake never
//     simulates the attempt finishing, because the real door cannot either.
type fakeDoor struct {
	mu sync.Mutex
	t  *testing.T

	// runID is the run the CREDENTIAL names. The door never reads a run id
	// from a path or a body the caller chose, so neither does the fake.
	runID   string
	project string

	// statuses is the container state per identity key (the job id), the
	// answer the named-container lookup would give.
	statuses map[string]doorStatus

	// keysByAttempt, when set, makes the fake the door BEFORE it keyed by
	// job: it ignores the caller's job id and answers for the attempt's own
	// job — the door the hn6 cloud-run stall ran against, whose settled
	// implement attempt answered for every repair of it.
	keysByAttempt bool

	// mintJobID and answerJobID, when set, are the job id the start route
	// mints and the status route answers for INSTEAD of the one asked about —
	// a door that disagrees with the caller about whose job this is.
	mintJobID   string
	answerJobID string

	// running is the model each identity's LIVE work process is on, keyed by
	// identity: what a fresh start's answer RECORDS (the door boots the
	// container on the request's model) and what an adoption reports back
	// (tick dyo) — the RUNNING container's model, never the new request's
	// echo. Seeded by adoptRunning for a container an OLDER deployment
	// booted, the one adoption shape no start of this door can produce.
	running map[string]string

	// script, when set, is answered INSTEAD of the door's own answer — a
	// refusal the route would forward (401, 409, 503) or a body the door
	// would never send, to prove the client fails loudly rather than
	// half-reads.
	script func(w http.ResponseWriter, r *http.Request)

	// bootedModel, when set, is the model the door says it booted the
	// container on INSTEAD of the one the request carried — a door (or an
	// adoption) that disagrees with the caller, for the tests that prove the
	// client refuses a handle naming a model it did not ask for.
	bootedModel string

	// bootedHarness, when set, is the harness the door says it booted the
	// container on INSTEAD of the one the request carried — the same
	// disagreement, for the harness cross-check (tick 9iz).
	bootedHarness string

	starts      int
	statusReads int
	lastBody    map[string]any
	lastAuth    string
	server      *httptest.Server
}

// doorStatus is what the named container looks like to the door.
type doorStatus struct {
	state        string
	terminal     bool
	observations []subprocess.Observation
}

func newFakeDoor(t *testing.T) *fakeDoor {
	t.Helper()
	door := &fakeDoor{
		t:        t,
		runID:    "r1",
		project:  "example/project",
		statuses: map[string]doorStatus{},
		running:  map[string]string{},
	}
	door.server = httptest.NewServer(door)
	t.Cleanup(door.server.Close)
	return door
}

// URL is the door's base URL, the FACTORY_BASE_URL a container boot exports.
func (d *fakeDoor) URL() string { return d.server.URL }

// identity is the job a request is about, the way the door keys it: the
// caller's job id, or the attempt's own when it names none (or when the fake
// is the door that ignored it). ok is false for a job id the real door
// refuses — one that is not this run's.
func (d *fakeDoor) identity(tickID string, attempt int, jobID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if jobID == "" || d.keysByAttempt {
		return d.jobIDLocked(tickID, attempt), true
	}
	return jobID, strings.HasPrefix(jobID, "run-"+d.runID+"/")
}

// setStatus tells the door what its named-container lookup answers for one
// attempt's own job: running, finished with a result, or absent.
func (d *fakeDoor) setStatus(tickID string, attempt int, status doorStatus) {
	d.setJobStatus(d.jobID(tickID, attempt), status)
}

// setJobStatus is setStatus for any job, by its full job id.
func (d *fakeDoor) setJobStatus(jobID string, status doorStatus) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statuses[jobID] = status
}

// adoptRunning seeds a LIVE work process under one identity, already on a
// model: the container an older deployment booted, from this door's point of
// view. A start under that identity finds it and must answer for the model it
// is on — the truthful-adoption case the tests that refuse it are about.
func (d *fakeDoor) adoptRunning(tickID string, attempt int, model string) {
	jobID := d.jobID(tickID, attempt)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running[jobID] = model
}

// startCount is how many starts the door was asked for.
func (d *fakeDoor) startCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts
}

// statusCount is how many state reads the door was asked for.
func (d *fakeDoor) statusCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusReads
}

// lastStartBody is the most recent start request the door read.
func (d *fakeDoor) lastStartBody() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastBody
}

// lastAuthorization is the credential the door most recently saw.
func (d *fakeDoor) lastAuthorization() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastAuth
}

// disagree makes the door mint (start) or answer for (status) a job other
// than the one asked about — the door's own view of whose job this is, for
// the tests that make it disagree with the caller's.
func (d *fakeDoor) disagree(mint, answer string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mintJobID, d.answerJobID = mint, answer
}

// keyByAttempt makes the fake the door before it keyed by job (see
// keysByAttempt).
func (d *fakeDoor) keyByAttempt() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keysByAttempt = true
}

// jobID is the job id the door derives for an identity — from the
// CREDENTIAL's run, never from the body, the way attemptJobID does on the
// real side.
func (d *fakeDoor) jobID(tickID string, attempt int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobIDLocked(tickID, attempt)
}

func (d *fakeDoor) jobIDLocked(tickID string, attempt int) string {
	return fmt.Sprintf("run-%s/tick-%s/attempt-%d", d.runID, tickID, attempt)
}

// refuseInvalid answers the real route's refusal of a job id that is not the
// credential's run's.
func refuseInvalid(w http.ResponseWriter, jobID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_request",
		"detail": fmt.Sprintf("job_id %q is not a job of this run", jobID)})
}

func (d *fakeDoor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.lastAuth = r.Header.Get("Authorization")
	rest := strings.TrimPrefix(r.URL.Path, doorPathAttempts)
	d.mu.Unlock()

	if r.URL.Path == doorPathStart {
		d.serveStart(w, r)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 2 && r.Method == http.MethodGet {
		d.serveStatus(w, r, parts[0], parts[1])
		return
	}
	http.Error(w, "not the sandbox dispatch door", http.StatusNotFound)
}

// serveStart answers POST /api/sandbox/attempts: one attempt's worker
// container, named by the attempt's identity, answered with a handle.
func (d *fakeDoor) serveStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	if d.script != nil {
		script := d.script
		d.mu.Unlock()
		script(w, r)
		return
	}
	d.mu.Unlock()

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	tickID, _ := body["tick_id"].(string)
	attempt := 0
	if n, ok := body["attempt"].(float64); ok {
		attempt = int(n)
	}
	asked, _ := body["job_id"].(string)
	jobID, ok := d.identity(tickID, attempt, asked)
	if !ok {
		refuseInvalid(w, asked)
		return
	}

	d.mu.Lock()
	d.starts++
	d.lastBody = body
	running, adopted := d.running[jobID]
	d.mu.Unlock()
	if adopted {
		// The named container already holds a live work process: the SAME
		// attempt comes back, adopted, on the model that process is on — the
		// recorded model of the boot that started it (tick dyo), never the
		// model this request carried.
		answer := map[string]any{"handle": d.handleBodyFor(jobID, tickID, attempt, body, running,
			"adopted: this container's work process was already running"), "adopted": true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(answer)
		return
	}
	// The model a FRESH boot of this body lands on: the request's own, unless
	// the test's bootedModel override says the door disagrees — a door that
	// boots on another model, for the tests that prove the client refuses a
	// handle naming a model it did not ask for. Either way it is what the
	// door's world RECORDS as running under the identity, which is what an
	// adoption under the same identity reports back (tick dyo).
	d.mu.Lock()
	model := body["model"].(string)
	if d.bootedModel != "" {
		model = d.bootedModel
	}
	d.running[jobID] = model
	d.mu.Unlock()

	handle := d.handleBodyFor(jobID, tickID, attempt, body, model, "dispatch confirmed")
	answer := map[string]any{"handle": handle, "adopted": false}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(answer)
}

// serveStatus answers GET /api/sandbox/attempts/:tick_id/:attempt: the state
// of the named sandbox, re-addressed by identity on every look.
func (d *fakeDoor) serveStatus(w http.ResponseWriter, r *http.Request, tickID, attemptText string) {
	attempt := 0
	if _, err := fmt.Sscanf(attemptText, "%d", &attempt); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	d.statusReads++
	if d.script != nil {
		script := d.script
		d.mu.Unlock()
		script(w, r)
		return
	}
	d.mu.Unlock()
	asked := r.URL.Query().Get("job_id")
	jobID, valid := d.identity(tickID, attempt, asked)
	if !valid {
		refuseInvalid(w, asked)
		return
	}
	d.mu.Lock()
	status, ok := d.statuses[jobID]
	if d.answerJobID != "" {
		jobID = d.answerJobID
	}
	d.mu.Unlock()

	answer := map[string]any{
		"schema_version": subprocess.SchemaVersion,
		"job_id":         jobID,
		"state":          subprocess.StateLost,
		"terminal":       false,
		"observed_at":    "2026-09-22T18:00:00Z",
		"cursor":         nil,
	}
	if ok {
		answer["state"] = status.state
		answer["terminal"] = status.terminal
		if len(status.observations) > 0 {
			answer["observations"] = status.observations
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// handleBodyFor mints the job_handle record the start route answers with:
// the contract's closed top level and this substrate's private addressing
// in the one open handle object, the same field set SandboxHandlePayload
// names — on the MODEL GIVEN, which is the model the container the answer
// is about is on: the request's for a fresh boot, the recorded boot's for an
// adoption (tick dyo).
func (d *fakeDoor) handleBodyFor(jobID, tickID string, attempt int, body map[string]any, model, detail string) map[string]any {
	role, _ := body["role"].(string)
	writeRef, _ := body["write_ref"].(string)
	baseRef, _ := body["base_ref"].(string)
	title, _ := body["title"].(string)
	baseSHA, _ := body["base_sha"].(string)
	epic, _ := body["epic"].(string)
	// The harness the answer names is the one the door's boot binds: the
	// request's own (tick 9iz), unless the test's bootedHarness override says
	// the door disagrees. The model is the caller's, per the adoption rule
	// above (tick dyo).
	harness, _ := body["harness"].(string)
	d.mu.Lock()
	if d.bootedHarness != "" {
		harness = d.bootedHarness
	}
	minted := jobID
	if d.mintJobID != "" {
		minted = d.mintJobID
	}
	runID := d.runID
	canonical := d.jobIDLocked(tickID, attempt)
	d.mu.Unlock()
	// One container per JOB: the attempt's own job keeps the name it always
	// had, and any other job's name carries its job id, so two jobs of one
	// attempt are told apart by what they address.
	sandbox := fmt.Sprintf("%s-%s-%d", runID, tickID, attempt)
	if jobID != canonical {
		sandbox += "-" + strings.NewReplacer("/", "-").Replace(jobID)
	}
	processID := "proc-" + strings.NewReplacer("/", "-").Replace(jobID)
	return map[string]any{
		"schema_version": subprocess.SchemaVersion,
		"job_id":         minted,
		"attempt":        attempt,
		"executor":       ExecutorName,
		"issued_at":      "2026-09-22T18:00:00Z",
		"handle": map[string]any{
			"sandbox":    sandbox,
			"process_id": processID,
			"base_sha":   baseSHA,
			"branch":     strings.TrimPrefix(writeRef, "refs/heads/"),
			"write_ref":  writeRef,
			"launched":   true,
			"detail":     detail,
			"run_id":     runID,
			"epic_id":    epic,
			"tick_id":    tickID,
			"role":       role,
			"project":    d.project,
			"base_ref":   baseRef,
			"title":      title,
			"model":      model,
			"harness":    harness,
		},
	}
}

// refuseWith scripts the door's answer for the next request: the status, the
// error class and the detail the real route's refusals carry.
func (d *fakeDoor) refuseWith(status int, class, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": class, "detail": detail})
	}
}

// answerGarbage scripts a body the door would never send, to prove the
// client surfaces an unclassifiable refusal rather than swallowing it.
func (d *fakeDoor) answerGarbage(status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("<html>not json</html>"))
	}
}

// ---------------------------------------------------------------- harness ---

// testModel is the model the harness's profile resolved: pi's spelling of GLM
// 5.3, the cloud profiles' own.
const testModel = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

// testHarness is the harness the harness's profile resolved: the cloud
// profiles' own runner, pi (tick 9iz).
const testHarness = "pi"

// testPrompt is the rendered role prompt the harness's profile resolved: the
// shape the cloud profiles carry — markdown prose, lines, a report contract —
// and deliberately nothing the container could derive for itself.
const testPrompt = "# implement-tick\n\n" +
	"You are implementing ONE unit of work from the ticks tracker, headless, in\n" +
	"an isolated git worktree that is yours alone. Nobody will answer a question.\n\n" +
	"Work test-first, and end your report with a STATUS line.\n"

// harness is one executor pointed at one door, with the spec the reconciler
// would build for one tick.
type harness struct {
	*testing.T
	door  *fakeDoor
	ex    *Executor
	spec  *subprocess.JobSpec
	state string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessAt(t, newFakeDoor(t))
}

// newHarnessAt points one harness at an EXISTING door with a FRESH state
// root — the process a later leg is: the same substrate, nothing the earlier
// process held in memory. The tick id stays "keh" unless a test says
// otherwise, because the identity under test is the attempt's, not the
// harness's.
func newHarnessAt(t *testing.T, door *fakeDoor) *harness {
	t.Helper()
	h := &harness{T: t}
	h.door = door
	h.state = t.TempDir()
	h.newExecutor(h.state)
	h.spec = h.newSpec("keh")
	return h
}

// newExecutor points a FRESH executor at the door — the process a later leg
// is, holding nothing the earlier one held in memory.
func (h *harness) newExecutor(state string) *Executor {
	h.Helper()
	ex, err := New(Options{
		FactoryURL: h.door.URL(),
		Token:      "run-r1-token",
		EpicID:     "xte",
		BaseRef:    "epic/xte",
		Title:      "A cloudflare-sandbox executor that returns a handle, not a result",
		Model:      testModel,
		Harness:    testHarness,
		Prompt:     testPrompt,
		Attempt:    1,
		StateDir:   state,
		// The run's stuck window (tick xba), the way the reconciler hands
		// every executor one: the dispatch's StuckAfter, defaulted.
		StuckAfter: 15 * time.Minute,
		Now:        func() time.Time { return time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		h.Fatalf("New: %v", err)
	}
	h.ex = ex
	return ex
}

// newSpec is a JobSpec the way the reconciler builds one: the job id in the
// door's own run/tick/attempt shape, one tick input, a write ref inside the
// grant's namespace.
func (h *harness) newSpec(tickID string) *subprocess.JobSpec {
	h.Helper()
	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", h.door.runID, tickID, 1)
	return &subprocess.JobSpec{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         jobID,
		Role:          "implement-tick",
		Source: subprocess.Source{
			Repository: "https://github.com/example/project",
			BaseSHA:    "0123456789abcdef0123456789abcdef01234567",
			WriteRef:   "refs/heads/ticfac/" + jobID,
		},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: tickID}, {Kind: "epic", ID: "xte"}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/" + jobID + "/",
		Credentials: subprocess.Credentials{
			Model: subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: subprocess.SourceCredential{Grant: &subprocess.SourceGrant{
				Issuer: "host", Grade: "write", WriteRefPrefix: "refs/heads/ticfac/",
			}},
		},
		Limits: subprocess.Limits{WallSeconds: 300},
	}
}

// start is one Start under the harness's spec and attempt.
func (h *harness) start(tickID string) (*subprocess.JobHandle, error) {
	return h.ex.Start(h.newSpec(tickID))
}

// running tells the door the named container holds a live work process.
func (h *harness) running(tickID string) {
	h.door.setStatus(tickID, 1, doorStatus{state: subprocess.StateRunning, terminal: false})
}

// settled tells the door the named container finished with a result.
func (h *harness) settled(tickID string, state string) {
	h.door.setStatus(tickID, 1, doorStatus{
		state:        state,
		terminal:     true,
		observations: []subprocess.Observation{{At: "2026-09-22T18:00:00Z", Kind: subprocess.ObsExited, Detail: "the container's work process exited 0"}},
	})
}
