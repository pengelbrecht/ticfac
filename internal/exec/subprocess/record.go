package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The protocol records of contracts/job-protocol.json, as Go types.
//
// Records are CLOSED (`additionalProperties: false` on every one of them and
// on every nested object except JobHandle.handle), so decoding is strict:
// ParseJobSpec refuses an unknown field rather than ignoring it. That is the
// contract's own rule and the opposite of the tk --json manifest's — an
// executor record is exchanged between two components that ship together,
// where a field one side invents and the other ignores IS the bug.
//
// The refusals these types produce are ticfac's, not the bundle validator's;
// record_test.go pins them to the fixture by requiring that every negative
// job_spec document in job-protocol.json is refused HERE too, so the two
// cannot drift into disagreeing about what a valid spec is.

// SchemaVersion is the one version every record in this protocol carries.
const SchemaVersion = 1

// ExecutorName is this executor's name on the seam. An executor NAME crosses
// it; a concrete backend name never does.
const ExecutorName = "local-subprocess"

// The schema ids each record declares, spelled as the contract spells them.
const (
	SchemaIDJobSpec    = "ticfac.job-spec.v1"
	SchemaIDJobHandle  = "ticfac.job-handle.v1"
	SchemaIDJobStatus  = "ticfac.job-status.v1"
	SchemaIDCancelAck  = "ticfac.cancel-ack.v1"
	SchemaIDJobResult  = "ticfac.job-result.v1"
	SchemaIDRoleResult = "ticfac.role-result.v1"
)

// ---------------------------------------------------------------- JobSpec ---

// JobSpec is what to run: the role, the source revision, the requested
// capabilities, the inputs, the output contract, the artifact destination, the
// credential grant and the limits.
type JobSpec struct {
	SchemaVersion  int          `json:"schema_version"`
	JobID          string       `json:"job_id"`
	Role           string       `json:"role"`
	Source         Source       `json:"source"`
	Capabilities   Capabilities `json:"capabilities"`
	Inputs         []Input      `json:"inputs"`
	OutputSchema   string       `json:"output_schema"`
	ArtifactPrefix string       `json:"artifact_prefix"`
	Credentials    Credentials  `json:"credentials"`
	Limits         Limits       `json:"limits"`
}

// Source names the repository, the revision to start from, and the ONE ref
// this job may write. Naming the ref in the spec is what lets a read-only
// source grade be enforced by the issuer rather than trusted from the runner.
type Source struct {
	Repository string `json:"repository"`
	BaseSHA    string `json:"base_sha"`
	WriteRef   string `json:"write_ref"`
}

// Capabilities is a REQUEST, not a backend selection. This executor translates
// it to a worktree and a subprocess.
type Capabilities struct {
	Persistence string `json:"persistence"`
	Isolation   string `json:"isolation"`
	Network     string `json:"network"`
}

// Input is one thing the job is about.
type Input struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Limits bounds the job. wall_seconds always binds; max_cost_usd binds only a
// metered credential, which is why it is optional here and REQUIRED to be
// absent-or-null in a flat-rate grant's budget_field.
type Limits struct {
	WallSeconds int      `json:"wall_seconds"`
	MaxCostUSD  *float64 `json:"max_cost_usd,omitempty"`
}

// Credentials is both halves, and both are required: a JobSpec that names no
// source grade is not "unrestricted", it is unreviewable.
type Credentials struct {
	Model  ModelCredential  `json:"model"`
	Source SourceCredential `json:"source"`
}

// Cost is a model grant's cost class. budget_field is REQUIRED in both forms
// and is null in a flat-rate one, so that "no budget" and "budget forgotten"
// cannot look identical.
type Cost struct {
	Class        string  `json:"class"`
	BudgetField  *string `json:"budget_field"`
	Telemetry    string  `json:"telemetry,omitempty"`
	UnknownCost  string  `json:"unknown_cost,omitempty"`
	QuotaFailure string  `json:"quota_failure,omitempty"`
}

// ModelGrant is the object form of a model credential.
type ModelGrant struct {
	Issuer   string `json:"issuer"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Cost     Cost   `json:"cost"`
}

// SourceGrant is the object form of a source credential.
type SourceGrant struct {
	Issuer         string `json:"issuer"`
	Grade          string `json:"grade"`
	WriteRefPrefix string `json:"write_ref_prefix,omitempty"`
}

// ModelCredential is the contract's anyOf: the shorthand "issued-by-host", or
// a grant that declares a cost class.
type ModelCredential struct {
	Shorthand string
	Grant     *ModelGrant
}

func (c ModelCredential) MarshalJSON() ([]byte, error) {
	if c.Grant != nil {
		return json.Marshal(c.Grant)
	}
	return json.Marshal(c.Shorthand)
}

func (c *ModelCredential) UnmarshalJSON(data []byte) error {
	var shorthand string
	if err := json.Unmarshal(data, &shorthand); err == nil {
		c.Shorthand, c.Grant = shorthand, nil
		return nil
	}
	var grant ModelGrant
	if err := strictUnmarshal(data, &grant); err != nil {
		return fmt.Errorf("credentials.model: %w", err)
	}
	c.Shorthand, c.Grant = "", &grant
	return nil
}

// SourceCredential is the contract's anyOf: the grade alone, or a grant that
// also bounds the ref namespace a write grade may advance.
type SourceCredential struct {
	Shorthand string
	Grant     *SourceGrant
}

func (c SourceCredential) MarshalJSON() ([]byte, error) {
	if c.Grant != nil {
		return json.Marshal(c.Grant)
	}
	return json.Marshal(c.Shorthand)
}

func (c *SourceCredential) UnmarshalJSON(data []byte) error {
	var shorthand string
	if err := json.Unmarshal(data, &shorthand); err == nil {
		c.Shorthand, c.Grant = shorthand, nil
		return nil
	}
	var grant SourceGrant
	if err := strictUnmarshal(data, &grant); err != nil {
		return fmt.Errorf("credentials.source: %w", err)
	}
	c.Shorthand, c.Grant = "", &grant
	return nil
}

// Grade is the source grade whichever form the credential took. It is the
// security boundary: read-only means this executor issues no push credential,
// so git write is refused by the ISSUER and not by the runner's good manners.
func (c SourceCredential) Grade() string {
	if c.Grant != nil {
		return c.Grant.Grade
	}
	return c.Shorthand
}

// WriteRefPrefix is the only ref namespace a write grade may advance. Empty
// when the grant does not bound one.
func (c SourceCredential) WriteRefPrefix() string {
	if c.Grant != nil {
		return c.Grant.WriteRefPrefix
	}
	return ""
}

// --------------------------------------------------------------- JobHandle ---

// JobHandle is a stable, opaque, re-addressable identity for a started job.
// Re-addressable means a controller that restarted holding nothing but this
// record can inspect, cancel and collect.
type JobHandle struct {
	SchemaVersion int            `json:"schema_version"`
	JobID         string         `json:"job_id"`
	Attempt       int            `json:"attempt"`
	Executor      string         `json:"executor"`
	Handle        map[string]any `json:"handle"`
	IssuedAt      string         `json:"issued_at"`
}

// LocalHandle is this executor's private addressing, carried inside
// JobHandle.handle — the ONE open object in the contract, because a Cloudflare
// workspace id and a local pid have nothing in common.
//
// pid, worktree and branch are spelled as job-protocol.json's own
// local-subprocess golden spells them.
type LocalHandle struct {
	PID        int    `json:"pid"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch"`
	Repo       string `json:"repo"`
	RepoKey    string `json:"repo_key"`
	State      string `json:"state"`
	ResultPath string `json:"result_path"`
	BaseSHA    string `json:"base_sha"`
	WriteRef   string `json:"write_ref"`
}

// Local decodes the executor-private half of a handle.
func (h *JobHandle) Local() (*LocalHandle, error) {
	if h == nil {
		return nil, fmt.Errorf("no handle")
	}
	if h.Executor != "" && h.Executor != ExecutorName {
		return nil, fmt.Errorf("handle names executor %q; this is %s", h.Executor, ExecutorName)
	}
	raw, err := json.Marshal(h.Handle)
	if err != nil {
		return nil, err
	}
	var local LocalHandle
	if err := json.Unmarshal(raw, &local); err != nil {
		return nil, fmt.Errorf("handle payload: %w", err)
	}
	if local.State == "" {
		return nil, fmt.Errorf("handle payload carries no state directory: it cannot be re-addressed")
	}
	return &local, nil
}

func (l *LocalHandle) asMap() map[string]any {
	raw, err := json.Marshal(l)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// --------------------------------------------------------------- JobStatus ---

// The lifecycle states. `lost` is deliberately NOT terminal: it says the
// executor can no longer address the handle, which is a statement about the
// observer and not about the job.
const (
	StatePending   = "pending"
	StateStarting  = "starting"
	StateRunning   = "running"
	StateLost      = "lost"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Observation kinds.
const (
	ObsStarted           = "started"
	ObsPromoted          = "promoted"
	ObsHeartbeat         = "heartbeat"
	ObsCredentialIssued  = "credential_issued"
	ObsCredentialRevoked = "credential_revoked"
	ObsCancelRequested   = "cancel_requested"
	ObsExited            = "exited"
)

// JobStatus reports what the executor can SEE, not what the reconciler
// concludes. `terminal` is redundant with `state` on purpose, and the contract
// cross-checks them: a disagreement is a liveness bug worth failing on.
type JobStatus struct {
	SchemaVersion int           `json:"schema_version"`
	JobID         string        `json:"job_id"`
	State         string        `json:"state"`
	Terminal      bool          `json:"terminal"`
	ObservedAt    string        `json:"observed_at"`
	Cursor        *string       `json:"cursor"`
	Observations  []Observation `json:"observations,omitempty"`
}

// Observation is one thing the executor saw, with the time it saw it.
type Observation struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// terminalState says whether a state can still change. One function, so
// `state` and `terminal` cannot be set from two different opinions.
func terminalState(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// --------------------------------------------------------------- CancelAck ---

// CancelAck states the three facts a caller cannot verify for itself:
// credentials were revoked, in what order relative to the stop request, and
// that reissue is refused.
type CancelAck struct {
	SchemaVersion      int     `json:"schema_version"`
	JobID              string  `json:"job_id"`
	AcceptedAt         string  `json:"accepted_at"`
	CredentialsRevoked bool    `json:"credentials_revoked"`
	Order              string  `json:"order"`
	Reissue            string  `json:"reissue"`
	StopRequested      bool    `json:"stop_requested"`
	SalvageDeadline    *string `json:"salvage_deadline,omitempty"`
}

// The two values the acknowledgement is allowed to carry. Encoded as values
// rather than as two timestamps because a validator can refuse a wrong value
// and cannot refuse a wrong clock.
const (
	OrderRevokeThenStop = "revoke-then-stop"
	ReissueRefused      = "refused"
)

// --------------------------------------------------------------- JobResult ---

// Outcomes and failure classes.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"

	FailureQuotaExhausted     = "quota_exhausted"
	FailureCostBudgetExceeded = "cost_budget_exceeded"
	FailureWallClockExceeded  = "wall_clock_exceeded"
	FailureCredentialRefused  = "credential_refused"
	FailureRunnerError        = "runner_error"
	FailureInfrastructure     = "infrastructure_error"
)

// JobResult is terminal facts: outcome and failure class, the source refs the
// job actually produced, the structured role output, and references to
// artifacts and evidence.
type JobResult struct {
	SchemaVersion int           `json:"schema_version"`
	JobID         string        `json:"job_id"`
	Attempt       int           `json:"attempt"`
	Outcome       string        `json:"outcome"`
	FailureClass  string        `json:"failure_class,omitempty"`
	FinishedAt    string        `json:"finished_at"`
	Source        ResultSource  `json:"source"`
	RoleResult    *RoleResult   `json:"role_result,omitempty"`
	Artifacts     []ArtifactRef `json:"artifacts"`
	Evidence      []EvidenceRef `json:"evidence"`
}

// ResultSource states what the job did to the ref it was given. head_sha is
// null when the job produced no commit — the `no-commits` verdict as a fact,
// rather than inferred from a missing key.
type ResultSource struct {
	BaseSHA  string  `json:"base_sha"`
	WriteRef string  `json:"write_ref"`
	HeadSHA  *string `json:"head_sha"`
	Commits  int     `json:"commits"`
}

// RoleResult is the envelope every LLM job returns. `result` is open because
// its shape is the role contract named by schema_id; the envelope is what the
// protocol freezes.
type RoleResult struct {
	SchemaVersion int            `json:"schema_version"`
	SchemaID      string         `json:"schema_id"`
	Role          string         `json:"role"`
	Status        string         `json:"status"`
	Summary       string         `json:"summary"`
	Result        map[string]any `json:"result"`
	Evidence      []EvidenceRef  `json:"evidence,omitempty"`
	Decisions     []Decision     `json:"decisions,omitempty"`
}

// ArtifactRef points at something the job produced.
type ArtifactRef struct {
	Kind          string `json:"kind"`
	URI           string `json:"uri"`
	ContentDigest string `json:"content_digest"`
	Bytes         int    `json:"bytes,omitempty"`
}

// EvidenceRef cites a ticfac.evidence.v1 record by its key.
type EvidenceRef struct {
	Key            string `json:"key"`
	PersistenceURI string `json:"persistence_uri"`
	ContentDigest  string `json:"content_digest"`
}

// Decision is a choice the job made and why.
type Decision struct {
	Question string `json:"question"`
	Choice   string `json:"choice"`
	Reason   string `json:"reason"`
}

// ------------------------------------------------------------- validation ---

var (
	roles = []string{
		"plan-epic", "implement-tick", "review-epic", "triage-failure",
		"plan-repair", "resolve-conflict", "closeout-epic", "evaluate-goal",
	}
	persistenceValues = []string{"ephemeral", "durable"}
	isolationValues   = []string{"process", "isolate", "container"}
	networkValues     = []string{"none", "restricted", "open"}
	inputKinds        = []string{"tick", "epic", "run", "goal", "artifact", "evidence"}
	sourceGrades      = []string{"read-only", "write"}
)

// ParseJobSpec decodes a JobSpec STRICTLY and validates it. An unknown field
// is refused rather than ignored: the record is closed, and a JobSpec naming a
// concrete backend is exactly the document the contract's negatives exist for.
func ParseJobSpec(data []byte) (*JobSpec, error) {
	var spec JobSpec
	if err := strictUnmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("job_spec: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

// Validate refuses a JobSpec this executor will not run. Every refusal names
// the field, because "the spec was invalid" is the message that sends the next
// repair at the wrong problem (Appendix A #9).
func (s *JobSpec) Validate() error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("job_spec.schema_version is %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.JobID == "" {
		return fmt.Errorf("job_spec.job_id is empty: a job nobody can name is a job nobody can find")
	}
	if !oneOf(roles, s.Role) {
		return fmt.Errorf("job_spec.role %q is not one of %s", s.Role, strings.Join(roles, ", "))
	}
	if s.Source.Repository == "" {
		return fmt.Errorf("job_spec.source.repository is empty")
	}
	if s.Source.BaseSHA == "" {
		return fmt.Errorf("job_spec.source.base_sha is empty")
	}
	if s.Source.WriteRef == "" {
		return fmt.Errorf("job_spec.source.write_ref is empty: nothing states which ref this job may write")
	}
	if !oneOf(persistenceValues, s.Capabilities.Persistence) {
		return fmt.Errorf("job_spec.capabilities.persistence %q is not one of %s",
			s.Capabilities.Persistence, strings.Join(persistenceValues, ", "))
	}
	if !oneOf(isolationValues, s.Capabilities.Isolation) {
		return fmt.Errorf("job_spec.capabilities.isolation %q is not one of %s",
			s.Capabilities.Isolation, strings.Join(isolationValues, ", "))
	}
	if !oneOf(networkValues, s.Capabilities.Network) {
		return fmt.Errorf("job_spec.capabilities.network %q is not one of %s",
			s.Capabilities.Network, strings.Join(networkValues, ", "))
	}
	for i, in := range s.Inputs {
		if !oneOf(inputKinds, in.Kind) {
			return fmt.Errorf("job_spec.inputs[%d].kind %q is not one of %s", i, in.Kind, strings.Join(inputKinds, ", "))
		}
		if in.ID == "" {
			return fmt.Errorf("job_spec.inputs[%d].id is empty", i)
		}
	}
	if s.OutputSchema == "" {
		return fmt.Errorf("job_spec.output_schema is empty: nothing says what the result must satisfy")
	}
	if s.ArtifactPrefix == "" {
		return fmt.Errorf("job_spec.artifact_prefix is empty")
	}
	if err := s.Credentials.validate(); err != nil {
		return err
	}
	if s.Limits.WallSeconds <= 0 {
		return fmt.Errorf("job_spec.limits.wall_seconds is %d: an unbounded job is one nothing stops", s.Limits.WallSeconds)
	}
	return nil
}

func (c Credentials) validate() error {
	switch {
	case c.Model.Grant != nil:
		g := c.Model.Grant
		if g.Issuer != "host" {
			return fmt.Errorf("job_spec.credentials.model.issuer is %q: the host issues every credential a job holds", g.Issuer)
		}
		switch g.Cost.Class {
		case "metered":
			if g.Cost.BudgetField == nil || *g.Cost.BudgetField != "max_cost_usd" {
				return fmt.Errorf("job_spec.credentials.model.cost: a metered grant's budget_field must be max_cost_usd")
			}
			if g.Cost.Telemetry != "gateway" {
				return fmt.Errorf("job_spec.credentials.model.cost.telemetry is %q, want gateway", g.Cost.Telemetry)
			}
		case "flat-rate":
			if g.Cost.BudgetField != nil {
				return fmt.Errorf("job_spec.credentials.model.cost: a flat-rate seat has no per-request cost to bound, " +
					"so budget_field must be present and null")
			}
			if g.Cost.QuotaFailure != FailureQuotaExhausted {
				return fmt.Errorf("job_spec.credentials.model.cost.quota_failure is %q, want %s",
					g.Cost.QuotaFailure, FailureQuotaExhausted)
			}
		default:
			return fmt.Errorf("job_spec.credentials.model.cost.class %q is not one of metered, flat-rate", g.Cost.Class)
		}
	case c.Model.Shorthand == "issued-by-host":
	default:
		return fmt.Errorf("job_spec.credentials.model is %q: a job holds no credential it brought itself", c.Model.Shorthand)
	}

	switch {
	case c.Source.Grant != nil:
		g := c.Source.Grant
		if g.Issuer != "host" {
			return fmt.Errorf("job_spec.credentials.source.issuer is %q: the host issues every credential a job holds", g.Issuer)
		}
		if !oneOf(sourceGrades, g.Grade) {
			return fmt.Errorf("job_spec.credentials.source.grade %q is not one of %s", g.Grade, strings.Join(sourceGrades, ", "))
		}
		if g.Grade == "read-only" && g.WriteRefPrefix != "" {
			return fmt.Errorf("job_spec.credentials.source: a read-only grade carries no write_ref_prefix")
		}
	case oneOf(sourceGrades, c.Source.Shorthand):
	default:
		return fmt.Errorf("job_spec.credentials.source %q is not one of %s: a spec that names no source grade is "+
			"not unrestricted, it is unreviewable", c.Source.Shorthand, strings.Join(sourceGrades, ", "))
	}
	return nil
}

// ParseJobHandle decodes a JobHandle strictly. `handle` itself stays open —
// it is executor-private by design — but everything around it is closed.
func ParseJobHandle(data []byte) (*JobHandle, error) {
	var h JobHandle
	if err := strictUnmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("job_handle: %w", err)
	}
	if h.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("job_handle.schema_version is %d, want %d", h.SchemaVersion, SchemaVersion)
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

func strictUnmarshal(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing content after the record")
	}
	return nil
}

func oneOf(allowed []string, value string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}
