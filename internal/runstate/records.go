package runstate

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SchemaVersion is the envelope's schema_version, carried by every committed
// record (SPEC §10.4).
const SchemaVersion = 1

// The records are closed structures on purpose: the contract's schemas are
// `additionalProperties: false`, so a field these types cannot express is a
// field the bundle refuses. Nullable fields are pointers and carry no
// `omitempty` — a record that OMITS integration_ref and one that states it as
// null are different claims, and only the second is evidence.

// Provenance is `$defs.provenance` — where a record came from: which run,
// which refs, which phase, and what produced it.
//
// It is defined in contracts/job-protocol.json and copied into
// contracts/ticfac-run-state.json field for field; this is the one Go
// spelling of it, for the same reason the bundle has one JSON spelling.
type Provenance struct {
	RunID                 string  `json:"run_id"`
	TickID                *string `json:"tick_id"`
	Attempt               *int    `json:"attempt"`
	SourceRef             string  `json:"source_ref"`
	SourceSHA             string  `json:"source_sha"`
	IntegrationRef        *string `json:"integration_ref"`
	Phase                 Phase   `json:"phase"`
	Executor              *string `json:"executor"`
	WorkspaceID           *string `json:"workspace_id"`
	Backend               *string `json:"backend"`
	Role                  *string `json:"role"`
	ProfileDigest         *string `json:"profile_digest"`
	Model                 *string `json:"model"`
	ContextManifestDigest *string `json:"context_manifest_digest"`
}

// provenanceFields is every field of $defs.provenance, all of them required.
// A record that OMITS integration_ref and one that states it as null are
// different claims, and only the second is evidence — so the decoder refuses
// the first rather than reading a missing field as a null one.
var provenanceFields = []string{
	"run_id", "tick_id", "attempt", "source_ref", "source_sha", "integration_ref",
	"phase", "executor", "workspace_id", "backend", "role", "profile_digest",
	"model", "context_manifest_digest",
}

// UnmarshalJSON reads provenance as the closed, fully-required object the
// bundle defines: every field present, and no field the definition does not
// have. Without this, Go's zero values would quietly turn an omitted field into
// a null one, which is the difference the contract exists to keep.
func (p *Provenance) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	known := map[string]bool{}
	for _, name := range provenanceFields {
		known[name] = true
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("provenance: missing required property %q", name)
		}
	}
	for name := range fields {
		if !known[name] {
			return fmt.Errorf("provenance: unexpected property %q", name)
		}
	}
	type provenance Provenance // shed this method, keep the field tags
	var decoded provenance
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	*p = Provenance(decoded)
	return nil
}

// Phase is `$defs.phase`: the gate vocabulary, closed. Naming the phase is what
// makes a record reproducible — the same command against the same ref at a
// different phase answers a different question.
type Phase string

const (
	PhaseWorker     Phase = "worker"
	PhasePostWave   Phase = "post-wave"
	PhaseIntegrated Phase = "integrated"
	PhaseReview     Phase = "review"
	PhaseCloseout   Phase = "closeout"
)

// Phases is the closed vocabulary, in the contract's order.
var Phases = []Phase{PhaseWorker, PhasePostWave, PhaseIntegrated, PhaseReview, PhaseCloseout}

// Executors is `$defs.executor`. An executor NAME crosses this seam; a concrete
// backend name does not.
var Executors = []string{"local-subprocess", "herdr", "cloudflare-sandbox", "cloudflare-computer"}

// Roles is `$defs.role`. There is no universal Decision Agent: every LLM call
// is a bounded job with a role-specific contract.
var Roles = []string{
	"plan-epic", "implement-tick", "review-epic", "triage-failure",
	"plan-repair", "resolve-conflict", "closeout-epic", "evaluate-goal",
}

func (p Provenance) validate() error {
	if p.RunID == "" {
		return fmt.Errorf("provenance.run_id is empty")
	}
	if p.SourceRef == "" || p.SourceSHA == "" {
		return fmt.Errorf("provenance names no source: a record that does not name the ref and sha it was produced against proves nothing")
	}
	if !oneOf(string(p.Phase), phaseNames()) {
		return fmt.Errorf("provenance.phase %q is not one of the permitted values", p.Phase)
	}
	if p.Executor != nil && !oneOf(*p.Executor, Executors) {
		return fmt.Errorf("provenance.executor %q is not one of the permitted values", *p.Executor)
	}
	if p.Role != nil && !oneOf(*p.Role, Roles) {
		return fmt.Errorf("provenance.role %q is not one of the permitted values", *p.Role)
	}
	return nil
}

// State is a run's lifecycle state — `schemas.checkpoint.properties.state`,
// closed. A state meaning "in flight" that no other reconciler can settle is
// the exact failure this vocabulary is closed to prevent.
type State string

const (
	StateAdmitted    State = "admitted"
	StateDispatching State = "dispatching"
	StateRunning     State = "running"
	StateCollecting  State = "collecting"
	StateGating      State = "gating"
	StateIntegrating State = "integrating"
	StatePublishing  State = "publishing"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
	StateCancelled   State = "cancelled"
)

// States is the closed vocabulary, in the contract's order.
var States = []State{
	StateAdmitted, StateDispatching, StateRunning, StateCollecting, StateGating,
	StateIntegrating, StatePublishing, StateCompleted, StateFailed, StateCancelled,
}

// Terminal reports whether the run is over. A terminal checkpoint is what
// places the run tag — and a failed or cancelled run's history is as worth
// keeping as a successful one's.
func (s State) Terminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// TickState is `$defs.tick_state`: one tick's position within a run.
type TickState struct {
	TickID  string `json:"tick_id"`
	State   string `json:"state"`
	Attempt int    `json:"attempt,omitempty"`
}

// TickStates is the closed vocabulary of `$defs.tick_state.properties.state`.
var TickStates = []string{"ready", "dispatched", "reported", "integrated", "rejected", "closed"}

// Checkpoint is the run's one mutable record, at
// `.ticfac/runs/<run-id>/checkpoint.json`. Written on state change, never on
// observation, and updated under a sha guard.
type Checkpoint struct {
	SchemaVersion int         `json:"schema_version"`
	RunID         string      `json:"run_id"`
	EpicID        string      `json:"epic_id"`
	Sequence      int         `json:"sequence"`
	State         State       `json:"state"`
	Reason        string      `json:"reason"`
	UpdatedAt     string      `json:"updated_at"`
	Ticks         []TickState `json:"ticks,omitempty"`
	Provenance    Provenance  `json:"provenance"`
}

// Validate applies the parts of `schemas.checkpoint` a Go type cannot: the
// envelope's version, the closed state vocabulary, and a sequence that moved.
func (c Checkpoint) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("checkpoint schema_version is %d, want %d", c.SchemaVersion, SchemaVersion)
	}
	if c.RunID == "" || c.EpicID == "" {
		return fmt.Errorf("checkpoint names no run or epic")
	}
	if c.Sequence < 1 {
		return fmt.Errorf("checkpoint sequence %d is not monotonic within the run", c.Sequence)
	}
	if !oneOf(string(c.State), stateNames()) {
		return fmt.Errorf("checkpoint state %q is not one of the permitted values", c.State)
	}
	if c.Reason == "" {
		return fmt.Errorf("checkpoint names no reason: a checkpoint is written on a state change, so there is always something to name")
	}
	if c.UpdatedAt == "" {
		return fmt.Errorf("checkpoint has no updated_at")
	}
	for i, ts := range c.Ticks {
		if ts.TickID == "" || !oneOf(ts.State, TickStates) {
			return fmt.Errorf("checkpoint ticks[%d] is %+v, which is not a tick state", i, ts)
		}
	}
	return c.Provenance.validate()
}

// Attempt is the dispatch marker, at `.ticfac/runs/<run-id>/attempts/<n>.json`.
// Its EXISTENCE is the idempotency marker: it is created if absent, and a
// second reconciler racing the same dispatch is refused by the repository.
type Attempt struct {
	SchemaVersion int            `json:"schema_version"`
	Attempt       int            `json:"attempt"`
	TickID        string         `json:"tick_id"`
	DispatchedAt  string         `json:"dispatched_at"`
	JobHandle     map[string]any `json:"job_handle"`
	Provenance    Provenance     `json:"provenance"`
}

// Validate applies `schemas.attempt`. The job handle is required and its shape
// is owned by the executor-protocol contract, so it is carried opaquely: what
// this contract requires is that the handle a dispatch was made with is
// recorded beside the marker that proves the dispatch happened.
func (a Attempt) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("attempt schema_version is %d, want %d", a.SchemaVersion, SchemaVersion)
	}
	if a.Attempt < 1 {
		return fmt.Errorf("attempt number %d is not 1-based", a.Attempt)
	}
	if a.TickID == "" {
		return fmt.Errorf("attempt names no tick")
	}
	if a.DispatchedAt == "" {
		return fmt.Errorf("attempt has no dispatched_at")
	}
	if a.JobHandle == nil {
		return fmt.Errorf("attempt carries no job_handle: without it nothing can inspect, cancel or collect the job it started")
	}
	return a.Provenance.validate()
}

// Decision is one role-job exchange, at
// `.ticfac/runs/<run-id>/decisions/<n>.json`. Created if absent: a validated
// decision is a thing a model was paid for once.
type Decision struct {
	SchemaVersion int            `json:"schema_version"`
	Decision      int            `json:"decision"`
	Role          string         `json:"role"`
	Request       map[string]any `json:"request"`
	Response      map[string]any `json:"response"`
	Validated     bool           `json:"validated"`
	RequestedAt   string         `json:"requested_at"`
	AnsweredAt    string         `json:"answered_at"`
	Provenance    Provenance     `json:"provenance"`
}

// Validate applies `schemas.decision`. An unvalidated model response landing as
// a decision is how a hallucinated wave gets dispatched, so an unvalidated
// response is refused here rather than written and read back as authority.
func (d Decision) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("decision schema_version is %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	if d.Decision < 1 {
		return fmt.Errorf("decision number %d is not 1-based", d.Decision)
	}
	if !oneOf(d.Role, Roles) {
		return fmt.Errorf("decision role %q is not one of the permitted values", d.Role)
	}
	if d.Request == nil || d.Response == nil {
		return fmt.Errorf("decision carries no request or no response")
	}
	if !d.Validated {
		return fmt.Errorf("decision is not validated: the VALIDATED response is what lands, so it can be re-read without re-asking a model")
	}
	if d.RequestedAt == "" || d.AnsweredAt == "" {
		return fmt.Errorf("decision has no requested_at or answered_at")
	}
	return d.Provenance.validate()
}

// Evidence is the record this contract PLACES and contracts/job-protocol.json
// DEFINES (`ticfac.evidence.v1`). The Go type follows that definition — nested
// provenance, closed, every field required — because bundle 1.2.0 shipped two
// shapes of it and no document satisfied both.
type Evidence struct {
	SchemaVersion  int        `json:"schema_version"`
	Key            string     `json:"key"`
	Provenance     Provenance `json:"provenance"`
	Check          Check      `json:"check"`
	StartedAt      string     `json:"started_at"`
	FinishedAt     string     `json:"finished_at"`
	ExitCode       *int       `json:"exit_code"`
	Output         Output     `json:"output"`
	Result         string     `json:"result"`
	Acceptance     string     `json:"acceptance"`
	ContentDigest  string     `json:"content_digest"`
	PersistenceURI string     `json:"persistence_uri"`
}

// Check is `$defs.check`: what produced the evidence.
type Check struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Command []string `json:"command,omitempty"`
}

// CheckResults is `$defs.check_result`. `error` is not `fail`: a gate whose
// command could not run has produced no evidence about the source ref.
var CheckResults = []string{"pass", "fail", "error", "skipped"}

// Acceptances is `$defs.acceptance`: whether the evidence gates closure or
// merely informs it.
var Acceptances = []string{"required", "advisory"}

// Output is the `anyOf` of `$defs.inline_output` and `$defs.artifact_output`,
// modelled as a closed union: exactly one of the two is set.
type Output struct {
	Inline   *InlineOutput
	Artifact *ArtifactOutput
}

// InlineOutput keeps stdout and stderr in the record, bounded and redacted.
type InlineOutput struct {
	Mode      string `json:"mode"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	Redacted  bool   `json:"redacted"`
	MaxBytes  int    `json:"max_bytes"`
}

// ArtifactOutput points at output kept outside the record.
type ArtifactOutput struct {
	Mode          string `json:"mode"`
	URI           string `json:"uri"`
	ContentDigest string `json:"content_digest"`
	Redacted      bool   `json:"redacted"`
	Bytes         int    `json:"bytes,omitempty"`
}

// MarshalJSON writes whichever arm of the union is set, and refuses both or
// neither: an `anyOf` with two arms satisfied is a record two readers disagree
// about.
func (o Output) MarshalJSON() ([]byte, error) {
	switch {
	case o.Inline != nil && o.Artifact != nil:
		return nil, fmt.Errorf("evidence output is both inline and artifact")
	case o.Inline != nil:
		return json.Marshal(o.Inline)
	case o.Artifact != nil:
		return json.Marshal(o.Artifact)
	default:
		return nil, fmt.Errorf("evidence output is neither inline nor artifact")
	}
}

// UnmarshalJSON dispatches on `mode`, which is the discriminator both arms
// carry as a single-valued enum.
func (o *Output) UnmarshalJSON(data []byte) error {
	var probe struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("evidence output: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	switch probe.Mode {
	case "inline":
		var v InlineOutput
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("evidence output (inline): %w", err)
		}
		*o = Output{Inline: &v}
	case "artifact":
		var v ArtifactOutput
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("evidence output (artifact): %w", err)
		}
		*o = Output{Artifact: &v}
	default:
		return fmt.Errorf("evidence output mode %q is neither inline nor artifact", probe.Mode)
	}
	return nil
}

// Validate applies the definition in contracts/job-protocol.json. It does not
// restate the shape — the shape is the Go type — but it does enforce the three
// closed vocabularies and the envelope.
func (e Evidence) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("evidence schema_version is %d, want %d", e.SchemaVersion, SchemaVersion)
	}
	if err := checkSegment("evidence key", e.Key); err != nil {
		return err
	}
	if e.Check.ID == "" || (e.Check.Kind != "command" && e.Check.Kind != "review") {
		return fmt.Errorf("evidence check %+v is not a command or a review check", e.Check)
	}
	if e.StartedAt == "" || e.FinishedAt == "" {
		return fmt.Errorf("evidence has no started_at or finished_at")
	}
	if !oneOf(e.Result, CheckResults) {
		return fmt.Errorf("evidence result %q is not one of the permitted values", e.Result)
	}
	if !oneOf(e.Acceptance, Acceptances) {
		return fmt.Errorf("evidence acceptance %q is not one of the permitted values", e.Acceptance)
	}
	if e.ContentDigest == "" || e.PersistenceURI == "" {
		return fmt.Errorf("evidence has no content_digest or persistence_uri")
	}
	if _, err := json.Marshal(e.Output); err != nil {
		return err
	}
	return e.Provenance.validate()
}

// Ptr is the nullable-field helper: `runstate.Ptr("refs/heads/epic/692")` for a
// field that is required and nullable.
func Ptr[T any](v T) *T { return &v }

func oneOf(value string, allowed []string) bool {
	for _, a := range allowed {
		if a == value {
			return true
		}
	}
	return false
}

func phaseNames() []string {
	out := make([]string, 0, len(Phases))
	for _, p := range Phases {
		out = append(out, string(p))
	}
	return out
}

func stateNames() []string {
	out := make([]string, 0, len(States))
	for _, s := range States {
		out = append(out, string(s))
	}
	return out
}

// encodeRecord is how every record reaches git: indented, newline-terminated,
// and with HTML escaping off, so a reason or a command line reads in `git show`
// as it was written.
func encodeRecord(record any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(record); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeRecord refuses a field the record type cannot express, which is the Go
// side of `additionalProperties: false`.
func decodeRecord(data []byte, record any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(record)
}
