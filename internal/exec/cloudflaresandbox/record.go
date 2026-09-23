package cloudflaresandbox

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The door's shapes, as Go: the request the start route reads, the handle it
// answers with, and the record this executor keeps beside them. The HTTP
// contract lives in ONE place — cloudflare/src/sandbox-dispatch.ts, tick 8ty
// — and this file is its other consumer, so every field the route documents
// is mirrored here and validated at the same shape, which is how two
// consumers of one mechanism catch drift instead of agreeing by accident.

// ExecutorName is this executor's name on the seam. An executor NAME crosses
// it; a concrete backend never does. It is what JobHandle.Executor carries,
// what a profile's `executor` field names for a cloud dispatch, and one of
// the names internal/runstate's closed `$defs.executor` list already admits.
const ExecutorName = "cloudflare-sandbox"

// PollInterval is the cadence at which a live job on this executor should be
// addressed. It is the CLOUD number — the same five minutes reconcile's own
// DefaultPollInterval states — because this substrate is one that can take
// an unaddressed job AWAY, and there the poll IS the keepalive (tick u9l,
// epic av8): the beat is the point, not latency to notice a settle. The
// reconciler takes this through KnownExecutor.PollInterval, so the interval
// belongs to the executor rather than to one global constant.
const PollInterval = 5 * time.Minute

// The field shapes the route refuses on, spelled here exactly as
// sandbox-dispatch.ts spells them so a spec this client would accept is one
// the door accepts too — a refusal that costs a round trip is a refusal the
// client should have made itself, and one the client fails to make is a
// dispatch that dies on the far side of HTTP with a message about shapes.
var (
	// tickIDPattern is TICK_ID_PATTERN: a tick id names the CONTAINER, so it
	// gets a container name's conservatism rather than free text's tolerance.
	tickIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// plainFieldPattern is PLAIN_FIELD_PATTERN: role, write_ref and base_ref
	// are printable ASCII with no whitespace, bounded.
	plainFieldPattern = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
	// titleFieldPattern is TITLE_FIELD_PATTERN: the title may carry the
	// spaces prose needs, never control characters.
	titleFieldPattern = regexp.MustCompile(`^[\x20-\x7e]{1,512}$`)
	// baseSHAPattern is runs.ts's BASE_SHA_PATTERN: the full 40-hex commit
	// the container clones at, refused rather than parsed.
	baseSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// startRequest is the body of POST /api/sandbox/attempts. Every field the
// route reads is here and every field here is one the route reads: the body
// is closed on both sides of one HTTP call, and a field one side stops
// sending is a drift this file exists to catch.
type startRequest struct {
	Epic     string `json:"epic"`
	TickID   string `json:"tick_id"`
	Attempt  int    `json:"attempt"`
	Role     string `json:"role"`
	WriteRef string `json:"write_ref"`
	BaseRef  string `json:"base_ref"`
	Title    string `json:"title"`
	BaseSHA  string `json:"base_sha"`
}

// startResponse is the door's answer to a start: the handle, and whether the
// named container was adopted rather than freshly dispatched. `adopted` is a
// first-class field on the route precisely so a caller never has to parse the
// fact back out of the handle's detail text.
type startResponse struct {
	Handle  *subprocess.JobHandle `json:"handle"`
	Adopted bool                  `json:"adopted"`
}

// cloudflareHandle is this executor's private addressing, carried inside
// JobHandle.Handle — the ONE open object in the contract. It is the door's
// SandboxHandlePayload with one addition this executor needs and the door
// does not: `state`, the local state directory a minimal {"state": …} handle
// (the shape the reconciler's adoption path synthesizes from an attempt
// record) resolves against.
//
// A handle carrying nothing but State is re-addressable: the attempt record
// in that directory holds the rest, and Inspect needs only the tick id and
// attempt number to ask the door — the run, as ever, comes from the
// credential.
type cloudflareHandle struct {
	// State is this executor's own attempt state directory, the one the
	// reconciler assigns. Not the door's: the door needs no local state,
	// this executor keeps one so its own Start/adopt decisions and the
	// reconciler's findAttemptState walk have durable evidence.
	State string `json:"state,omitempty"`

	// Everything below is the door's SandboxHandlePayload, spelled as
	// sandbox-executor.ts spells it. A null process_id decodes to nil, which
	// is a fact ("the dispatch was never confirmed to a process"), not a
	// missing key.
	Sandbox   string  `json:"sandbox"`
	ProcessID *string `json:"process_id"`
	BaseSHA   string  `json:"base_sha"`
	Branch    string  `json:"branch"`
	WriteRef  string  `json:"write_ref"`
	Launched  bool    `json:"launched"`
	Detail    string  `json:"detail"`
	RunID     string  `json:"run_id"`
	EpicID    string  `json:"epic_id"`
	TickID    string  `json:"tick_id"`
	Role      string  `json:"role"`
	Project   string  `json:"project"`
	BaseRef   string  `json:"base_ref"`
	Title     string  `json:"title"`
}

// local decodes the executor-private half of a handle, refusing one that
// names a different executor. A handle from another executor is not this
// package's to address, and quietly decoding it would be exactly the "one
// tick's job collected under another tick's name" failure the reconciler
// guards against on its side.
func local(h *subprocess.JobHandle) (*cloudflareHandle, error) {
	if h == nil {
		return nil, fmt.Errorf("no handle")
	}
	if h.Executor != "" && h.Executor != ExecutorName {
		return nil, fmt.Errorf("handle names executor %q; this is %s", h.Executor, ExecutorName)
	}
	if h.Handle == nil {
		return nil, fmt.Errorf("handle payload is absent: there is nothing to re-address")
	}
	raw, err := json.Marshal(h.Handle)
	if err != nil {
		return nil, err
	}
	// The payload is decoded LENIENTLY (an unknown field is ignored) because
	// the contract leaves it open: the door is free to add a field of
	// addressing, and a client that hard-refused a newer door's handles would
	// turn an addition into an outage. What is NOT lenient is the identity
	// half below — the fields Inspect needs are required, in this process or
	// resolved from the attempt record.
	var payload cloudflareHandle
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("handle payload: %w", err)
	}
	if payload.TickID != "" && !tickIDPattern.MatchString(payload.TickID) {
		return nil, fmt.Errorf("handle payload carries tick id %q, which is not a name the door reads", payload.TickID)
	}
	if payload.State == "" && payload.TickID == "" {
		return nil, fmt.Errorf("handle payload carries neither a state directory nor a tick id: " +
			"the door is addressed by identity, and this handle states none")
	}
	return &payload, nil
}

// resolved fills the identity a minimal handle does not carry from the
// attempt record in its state directory. The record is the authority: the
// handle is a frozen copy of it, and the copy is the thing a fresh
// controller could have lost.
func (c *cloudflareHandle) resolved() (*cloudflareHandle, *attemptRecord, error) {
	if c.State == "" {
		return c, nil, nil
	}
	record, err := newStore(c.State).readAttempt()
	if err != nil {
		return c, nil, fmt.Errorf("no attempt record at %s: %w", c.State, err)
	}
	full := *c
	if full.TickID == "" {
		full.TickID = record.TickID
	}
	if full.Sandbox == "" {
		full.Sandbox = record.Sandbox
	}
	if full.RunID == "" {
		full.RunID = record.RunID
	}
	if full.ProcessID == nil {
		full.ProcessID = record.ProcessID
	}
	if full.Branch == "" {
		full.Branch = record.Branch
	}
	if full.BaseSHA == "" {
		full.BaseSHA = record.BaseSHA
	}
	if full.WriteRef == "" {
		full.WriteRef = record.WriteRef
	}
	return &full, record, nil
}

// asMap is the handle's durable form.
func (c *cloudflareHandle) asMap() map[string]any {
	raw, err := json.Marshal(c)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// handleFor builds the protocol handle for one attempt: the closed fields
// from the record, this executor's private addressing in the one open object.
func handleFor(record *attemptRecord) *subprocess.JobHandle {
	return &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         record.JobID,
		Attempt:       record.Attempt,
		Executor:      ExecutorName,
		Handle:        record.payload().asMap(),
		IssuedAt:      record.IssuedAt,
	}
}

// The state schema and file names. attempt.json is the name the RECONCILER's
// findAttemptState walks for — changing it would strand every cloudflare
// attempt the moment a restart went looking for one.
const (
	stateSchemaVersion = 1
	fileAttempt        = "attempt.json"
)

// attemptRecord is the durable description of one attempt: the identity a
// restarted controller needs to re-address it through the door, and the
// handle the door minted, frozen. The door needs none of this — it
// re-addresses by name — but this executor's own Start decisions do: an
// attempt whose record exists is one whose dispatch once landed, and "the
// record exists and the door cannot address the container" is a HOLD, never
// a redispatch.
type attemptRecord struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	Attempt       int    `json:"attempt"`
	TickID        string `json:"tick_id"`
	State         string `json:"state"`

	// The door's handle payload, in full. The handle Start returned is the
	// frozen copy; this record is the thing a minimal {"state": …} handle
	// resolves against.
	Sandbox   string  `json:"sandbox"`
	ProcessID *string `json:"process_id"`
	BaseSHA   string  `json:"base_sha"`
	Branch    string  `json:"branch"`
	WriteRef  string  `json:"write_ref"`
	Launched  bool    `json:"launched"`
	Detail    string  `json:"detail,omitempty"`
	RunID     string  `json:"run_id"`
	EpicID    string  `json:"epic_id"`
	Role      string  `json:"role"`
	Project   string  `json:"project"`
	BaseRef   string  `json:"base_ref"`
	Title     string  `json:"title"`

	// Adopted says the door's start route found a live work process under
	// this identity and adopted it rather than booting a rival.
	Adopted bool `json:"adopted"`

	// Spec is the JobSpec this attempt was dispatched under, for the same
	// reason herdr's record carries it: an attempt record that cannot say
	// what was asked for is a record collect cannot answer for.
	Spec     *subprocess.JobSpec `json:"spec"`
	IssuedAt string              `json:"issued_at"`
}

// payload is the record's handle half.
func (r *attemptRecord) payload() *cloudflareHandle {
	return &cloudflareHandle{
		State:     r.State,
		Sandbox:   r.Sandbox,
		ProcessID: r.ProcessID,
		BaseSHA:   r.BaseSHA,
		Branch:    r.Branch,
		WriteRef:  r.WriteRef,
		Launched:  r.Launched,
		Detail:    r.Detail,
		RunID:     r.RunID,
		EpicID:    r.EpicID,
		TickID:    r.TickID,
		Role:      r.Role,
		Project:   r.Project,
		BaseRef:   r.BaseRef,
		Title:     r.Title,
	}
}

// parseHandle decodes the door's job_handle strictly. The top level is the
// contract's CLOSED record — schema_version, job_id, attempt, executor,
// handle, issued_at and nothing else — so an unknown field is a refusal, not
// a shrug: the door and this client are the two consumers of one mechanism,
// and a field one side invented is exactly the drift this file exists to
// catch. The payload inside stays open, as the contract leaves it.
func parseHandle(data []byte) (*subprocess.JobHandle, error) {
	var h subprocess.JobHandle
	if err := strictUnmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("job_handle: %w", err)
	}
	if h.SchemaVersion != subprocess.SchemaVersion {
		return nil, fmt.Errorf("job_handle.schema_version is %d, want %d", h.SchemaVersion, subprocess.SchemaVersion)
	}
	if h.JobID == "" {
		return nil, fmt.Errorf("job_handle.job_id is empty")
	}
	if h.Executor != ExecutorName {
		return nil, fmt.Errorf("job_handle.executor is %q; this is the %s executor", h.Executor, ExecutorName)
	}
	if h.Handle == nil {
		return nil, fmt.Errorf("job_handle.handle is absent: there is nothing to re-address")
	}
	return &h, nil
}

// parseStatus decodes the door's job_status strictly and cross-checks it:
// `terminal` is redundant with `state` ON PURPOSE, and a disagreement
// between the two is a liveness bug worth failing on rather than a field
// worth trusting (the contract's own anyOf). The job id is checked against
// the identity this client asked about, because the door re-derives it from
// the credential — a mismatch is the client and the door disagreeing about
// whose attempt this is, which is never a status to act on.
func parseStatus(data []byte, jobID string) (*subprocess.JobStatus, error) {
	var status subprocess.JobStatus
	if err := strictUnmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("job_status: %w", err)
	}
	if status.SchemaVersion != subprocess.SchemaVersion {
		return nil, fmt.Errorf("job_status.schema_version is %d, want %d", status.SchemaVersion, subprocess.SchemaVersion)
	}
	if status.JobID == "" {
		return nil, fmt.Errorf("job_status.job_id is empty")
	}
	if jobID != "" && status.JobID != jobID {
		return nil, fmt.Errorf("job_status.job_id is %q, want %q: the door answered for a different attempt",
			status.JobID, jobID)
	}
	if status.Terminal != terminalState(status.State) {
		return nil, fmt.Errorf("job_status says state %q with terminal=%v: the two disagree, and a status whose "+
			"terminal flag disagrees with its state is a liveness bug worth failing on", status.State, status.Terminal)
	}
	return &status, nil
}

// terminalState says whether a state can still change. One function, so
// `state` and `terminal` cannot be set from two different opinions — the
// same closed vocabulary subprocess owns, restated here because the cross
// check above has to ask it and the reconciler must not import this
// package to read an answer.
func terminalState(state string) bool {
	switch state {
	case subprocess.StateSucceeded, subprocess.StateFailed, subprocess.StateCancelled:
		return true
	default:
		return false
	}
}

// tickOf is the tick this job is about, from the spec's inputs — the same
// derivation herdr uses, because the inputs are the protocol's own way of
// saying it and job_id is OPAQUE: the contract says the reconciler owns its
// shape, so parsing an attempt out of it would be this side deciding what
// the other side's identifier means.
func tickOf(spec *subprocess.JobSpec) string {
	for _, in := range spec.Inputs {
		if in.Kind == "tick" {
			return in.ID
		}
	}
	for _, in := range spec.Inputs {
		return in.ID
	}
	return "job"
}

// validateDoorFields refuses a spec this door would refuse anyway, naming the
// field — a dispatch that dies on the far side of HTTP with a message about
// shapes is a round trip spent to learn what the client could have said
// itself.
func validateDoorFields(req *startRequest) error {
	if req.Epic == "" {
		return fmt.Errorf("the epic is not configured: the door checks it against the run's own row, " +
			"and a start with no epic to state is a start that cannot be checked")
	}
	if !tickIDPattern.MatchString(req.TickID) {
		return fmt.Errorf("tick id %q is not a name the door reads (alphanumerics, `.`, `_`, `-`; at most 64 "+
			"characters) — it is the name the attempt's container is addressed by", req.TickID)
	}
	if req.Attempt < 1 {
		return fmt.Errorf("attempt is %d: the door takes the positive integer that identifies this try", req.Attempt)
	}
	if !plainFieldPattern.MatchString(req.Role) {
		return fmt.Errorf("role %q is not a non-empty printable ASCII string the door reads", req.Role)
	}
	if !plainFieldPattern.MatchString(req.WriteRef) {
		return fmt.Errorf("write_ref %q is not a non-empty printable ASCII string the door reads", req.WriteRef)
	}
	if !plainFieldPattern.MatchString(req.BaseRef) {
		return fmt.Errorf("base_ref %q is not a non-empty printable ASCII string the door reads", req.BaseRef)
	}
	if !titleFieldPattern.MatchString(req.Title) {
		return fmt.Errorf("title is not a non-empty printable ASCII string the door reads (spaces allowed, at " +
			"most 512 characters)")
	}
	if !baseSHAPattern.MatchString(req.BaseSHA) {
		return fmt.Errorf("base_sha %q is not the full 40-character commit the attempt's container clones at", req.BaseSHA)
	}
	return nil
}
