package cloudflaresandbox

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The door's shapes, decoded: the strict halves refused loudly, the open half
// left open, and every field the route validates validated here too — so a
// spec this client would accept is one the door accepts, and a refusal that
// costs a round trip is a refusal the client should have made itself.

// short: pure parsing; no I/O.
func TestParseHandleRefusesDrift(t *testing.T) {
	good := `{
		"schema_version": 1,
		"job_id": "run-r1/tick-keh/attempt-1",
		"attempt": 1,
		"executor": "cloudflare-sandbox",
		"handle": {"sandbox": "r1-keh-1", "tick_id": "keh"},
		"issued_at": "2026-09-22T18:00:00Z"
	}`
	if _, err := parseHandle([]byte(good)); err != nil {
		t.Fatalf("a well-formed handle was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"unknown field", strings.Replace(good, `"issued_at": "2026-09-22T18:00:00Z"`, `"issued_at": "2026-09-22T18:00:00Z", "surprise": 1`, 1), "surprise"},
		{"wrong schema version", strings.Replace(good, `"schema_version": 1`, `"schema_version": 2`, 1), "schema_version"},
		{"wrong executor", strings.Replace(good, `"cloudflare-sandbox"`, `"local-subprocess"`, 1), "local-subprocess"},
		{"no handle payload", strings.Replace(good, `{"sandbox": "r1-keh-1", "tick_id": "keh"}`, `null`, 1), "absent"},
		{"no job id", strings.Replace(good, `"run-r1/tick-keh/attempt-1"`, `""`, 1), "job_id"},
	} {
		_, err := parseHandle([]byte(tc.body))
		if err == nil {
			t.Errorf("%s: the drifted handle was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not name the field: %v", tc.name, err)
		}
	}
}

// short: pure parsing; no I/O.
func TestParseStatusRefusesALyingTerminalFlag(t *testing.T) {
	good := `{
		"schema_version": 1,
		"job_id": "run-r1/tick-keh/attempt-1",
		"state": "running",
		"terminal": false,
		"observed_at": "2026-09-22T18:00:00Z",
		"cursor": null
	}`
	if _, err := parseStatus([]byte(good), "run-r1/tick-keh/attempt-1"); err != nil {
		t.Fatalf("a well-formed status was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		// `terminal` is redundant with `state` on purpose: a disagreement is
		// a liveness bug worth failing on, not a field worth trusting.
		{"succeeded but not terminal", strings.Replace(good, `"state": "running"`, `"state": "succeeded"`, 1), "disagree"},
		{"running but terminal", strings.Replace(good, `"terminal": false`, `"terminal": true`, 1), "disagree"},
		{"unknown field", strings.Replace(good, `"cursor": null`, `"cursor": null, "surprise": 1`, 1), "surprise"},
		{"no job id", strings.Replace(good, `"run-r1/tick-keh/attempt-1"`, `""`, 1), "job_id"},
	} {
		_, err := parseStatus([]byte(tc.body), "")
		if err == nil {
			t.Errorf("%s: the drifted status was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not name the problem: %v", tc.name, err)
		}
	}

	// The job id cross-check: the door re-derives it from the credential, and
	// a mismatch is never a status to act on.
	if _, err := parseStatus([]byte(good), "run-somebody-else/tick-keh/attempt-1"); err == nil {
		t.Error("a status answering for another job was accepted")
	}
}

// short: pure parsing; no I/O.
func TestValidateDoorFieldsRefusesWhatTheDoorRefuses(t *testing.T) {
	good := &startRequest{
		Epic:     "xte",
		TickID:   "keh",
		Attempt:  1,
		Role:     "implement-tick",
		WriteRef: "refs/heads/ticfac/run-r1/tick-keh/attempt-1",
		BaseRef:  "epic/xte",
		Title:    "A tick title with spaces",
		BaseSHA:  "0123456789abcdef0123456789abcdef01234567",
	}
	if err := validateDoorFields(good); err != nil {
		t.Fatalf("a request the door accepts was refused here: %v", err)
	}

	for _, tc := range []struct {
		name  string
		mutat func(r *startRequest)
		want  string
	}{
		{"no epic", func(r *startRequest) { r.Epic = "" }, "epic"},
		{"tick id is free text", func(r *startRequest) { r.TickID = "not a container name!" }, "tick id"},
		{"tick id too long", func(r *startRequest) { r.TickID = strings.Repeat("a", 65) }, "tick id"},
		{"tick id starts with a dot", func(r *startRequest) { r.TickID = ".keh" }, "tick id"},
		{"attempt is zero", func(r *startRequest) { r.Attempt = 0 }, "attempt"},
		{"role carries a space", func(r *startRequest) { r.Role = "implement tick" }, "role"},
		{"write ref carries a space", func(r *startRequest) { r.WriteRef = "refs/heads/one two" }, "write_ref"},
		{"base ref is empty", func(r *startRequest) { r.BaseRef = "" }, "base_ref"},
		{"title carries a newline", func(r *startRequest) { r.Title = "line\nline" }, "title"},
		{"base sha is short", func(r *startRequest) { r.BaseSHA = "abc123" }, "base_sha"},
	} {
		r := *good
		tc.mutat(&r)
		err := validateDoorFields(&r)
		if err == nil {
			t.Errorf("%s: the request the door would refuse was accepted here", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not name the field: %v", tc.name, err)
		}
	}
}

// short: pure parsing; no I/O.
func TestTickOfDerivesFromTheSpecInputs(t *testing.T) {
	spec := &subprocess.JobSpec{Inputs: []subprocess.Input{
		{Kind: "epic", ID: "xte"}, {Kind: "tick", ID: "keh"},
	}}
	if got := tickOf(spec); got != "keh" {
		t.Errorf("tickOf is %q, want keh: the tick input wins over the others", got)
	}
	spec = &subprocess.JobSpec{Inputs: []subprocess.Input{{Kind: "epic", ID: "xte"}}}
	if got := tickOf(spec); got != "xte" {
		t.Errorf("tickOf is %q, want xte: without a tick input, the first input is what names the job", got)
	}
	if got := tickOf(&subprocess.JobSpec{}); got != "job" {
		t.Errorf("tickOf is %q, want job: a spec with no inputs names nothing specific", got)
	}
}

// The handle payload is the contract's ONE open object: the door is free to
// add a field of addressing, and a client that hard-refused a newer door's
// handles would turn an addition into an outage. What is NOT lenient is the
// identity half — a tick id that is not a name the door reads is refused.
//
// short: pure parsing; no I/O.
func TestHandlePayloadDecodingIsLenientButIdentityIsNot(t *testing.T) {
	raw := `{"sandbox": "r1-keh-1", "tick_id": "keh", "tomorrow": "a field this client does not know"}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	h := &subprocess.JobHandle{Executor: ExecutorName, Handle: payload}
	decoded, err := local(h)
	if err != nil {
		t.Fatalf("a newer door's handle was refused: %v", err)
	}
	if decoded.Sandbox != "r1-keh-1" {
		t.Errorf("decoded sandbox is %q, want r1-keh-1", decoded.Sandbox)
	}

	bad := map[string]any{"tick_id": "not a container name!"}
	if _, err := local(&subprocess.JobHandle{Executor: ExecutorName, Handle: bad}); err == nil {
		t.Error("a payload carrying a tick id the door cannot read was accepted")
	}
	if _, err := local(&subprocess.JobHandle{Executor: ExecutorName, Handle: map[string]any{}}); err == nil {
		t.Error("a payload carrying neither state nor tick id was accepted")
	}
}

// short: one tempdir.
func TestResolvedFillsIdentityFromTheRecord(t *testing.T) {
	dir := t.TempDir()
	processID := "proc-1"
	record := &attemptRecord{
		SchemaVersion: stateSchemaVersion,
		JobID:         "run-r1/tick-keh/attempt-1",
		Attempt:       1,
		TickID:        "keh",
		Sandbox:       "r1-keh-1",
		ProcessID:     &processID,
		Spec:          &subprocess.JobSpec{},
	}
	if err := newStore(dir).writeAttempt(record); err != nil {
		t.Fatal(err)
	}

	// The minimal handle resolves EVERYTHING the door needs to be asked by.
	minimal := &cloudflareHandle{State: dir}
	full, _, err := minimal.resolved()
	if err != nil {
		t.Fatalf("resolve from the record: %v", err)
	}
	if full.TickID != "keh" || full.Sandbox != "r1-keh-1" {
		t.Errorf("resolved to tick %q container %q, want keh / r1-keh-1", full.TickID, full.Sandbox)
	}
	if full.ProcessID == nil || *full.ProcessID != "proc-1" {
		t.Errorf("resolved process id is %v, want proc-1", full.ProcessID)
	}

	// A full payload does not need the record, and never falls back to one
	// it cannot read.
	fixed := &cloudflareHandle{TickID: "keh", Sandbox: "r1-keh-1"}
	if _, _, err := fixed.resolved(); err != nil {
		t.Errorf("a payload with no state directory tried to resolve: %v", err)
	}
	missing := &cloudflareHandle{State: filepath.Join(dir, "nowhere")}
	if _, _, err := missing.resolved(); err == nil {
		t.Error("a state directory with no record resolved to nothing, silently")
	}
}
