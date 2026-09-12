package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The findings channel: how a worker reports something it discovered OUTSIDE
// its tick without writing the tracker and without prose an orchestrator has
// to read carefully (tick 7vn).
//
// The channel is a machine-readable block in the report the worker already
// writes — a fenced code block with the info string `findings` holding a
// JSON array of typed findings — lifted by collect into the role-result
// envelope, where `result` is the one object the contract leaves open for
// the role's own payload. The reconciler turns each finding into a DRAFT
// tick proposal; the worker never writes `.tick/`, which is a protected
// prefix, and the reconciler never opens a tick on a worker's word — the
// draft is triaged by a person, which is what keeps the scope decision human.
//
// The block is deliberately INSIDE the report rather than a second file: the
// report is the one deliverable collect already reads durably (off the branch
// when the worktree is gone), and a second channel is a second thing a worker
// can forget and a second reader the vocabulary drifts in.

// The closed vocabulary of finding kinds. A kind says what the finding IS, not
// where it goes — `target` says where it goes.
//
//	proposed-tick  a tick this repository should carry (the v3i shape: a
//	               blocked worker proposing the split it could not make)
//	upstream-tick  a tick ANOTHER repository should carry (the 604 shape: an
//	               upstream finding the orchestrator had to read prose to
//	               file) — requires a target repository
//	contract       a pinned contract bundle change (the 8ug shape)
//	defect         a defect outside this tick's scope that nobody dispatched
//	               work for
const (
	FindingKindProposedTick = "proposed-tick"
	FindingKindUpstreamTick = "upstream-tick"
	FindingKindContract     = "contract"
	FindingKindDefect       = "defect"
)

// FindingKinds is the closed kind vocabulary.
var FindingKinds = []string{
	FindingKindProposedTick, FindingKindUpstreamTick, FindingKindContract, FindingKindDefect,
}

// The closed severity vocabulary. A severity is a worker's claim about how much
// the finding matters; triage is where it is acted on, and a severity nobody
// validates is a severity nobody can sort by.
const (
	FindingSeverityLow    = "low"
	FindingSeverityMedium = "medium"
	FindingSeverityHigh   = "high"
)

// FindingSeverities is the closed severity vocabulary.
var FindingSeverities = []string{
	FindingSeverityLow, FindingSeverityMedium, FindingSeverityHigh,
}

// targetRepositoryPattern is the shape of a finding's target: an owner/name
// GitHub repository, e.g. pengelbrecht/ticks. An upstream finding belongs on a
// DIFFERENT tracker, so the target names which one; an empty target means the
// repository the run is working on.
var targetRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Finding is one thing a worker discovered outside its tick. Every field is
// REQUIRED in the report block — kind, title, body, severity and the target
// repository — with body and target allowed to be empty, so "no target" and
// "target forgotten" cannot look identical.
type Finding struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
}

// findingFields is every field of the record, for the closed-key check: a
// findings block carrying a field this record does not have is refused rather
// than read as if it were smaller.
var findingFields = []string{"kind", "title", "body", "severity", "target"}

// knownFindingField is the closed key set of the finding record.
var knownFindingField = map[string]bool{
	"kind": true, "title": true, "body": true, "severity": true, "target": true,
}

// Validate refuses a finding this channel will not carry. Every refusal names
// the field, because "the finding was invalid" is the message that sends the
// next worker at the wrong problem.
func (f Finding) Validate() error {
	if !oneOf(FindingKinds, f.Kind) {
		return fmt.Errorf("finding.kind %q is not one of %s", f.Kind, strings.Join(FindingKinds, ", "))
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("finding.title is empty: a finding nobody can name is one nobody can triage")
	}
	if !oneOf(FindingSeverities, f.Severity) {
		return fmt.Errorf("finding.severity %q is not one of %s", f.Severity, strings.Join(FindingSeverities, ", "))
	}
	if f.Target != "" && !targetRepositoryPattern.MatchString(f.Target) {
		return fmt.Errorf("finding.target %q is not an owner/name repository", f.Target)
	}
	if f.Kind == FindingKindUpstreamTick && f.Target == "" {
		return fmt.Errorf("finding.target is empty: an upstream tick belongs on another repository's tracker, " +
			"and a finding that names none is routed nowhere")
	}
	return nil
}

// findingsFence is the opening line of the findings block: a code fence with
// the info string `findings` and nothing else on the line.
var findingsFence = regexp.MustCompile("^```findings[ \\t]*$")

// anyFence is any code-fence line, which is what CLOSES the findings block.
var anyFence = regexp.MustCompile("^```")

// ParseFindings reads the findings block out of a report body.
//
// The FINAL complete block wins, for the same reason the FINAL status line
// does: a report may quote the template above its own answer. A block that
// opens and never closes is a problem rather than nothing — silently dropping
// a findings block is the exact failure (findings lost) this channel exists
// to remove, so a truncated one is reported as unparseable.
//
// The return is the typed list and, when the report carries a block this
// reader cannot accept, the problem with it. (nil, "") means the report
// carries no findings block at all; ([]Finding{}, "") is not a return value —
// a block that proposes nothing is the same as no block.
func ParseFindings(body string) (findings []Finding, problem string) {
	var last []string
	var block []string
	open := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case !open && findingsFence.MatchString(line):
			open, block = true, nil
		case open && anyFence.MatchString(line):
			// Keep scanning: the LAST complete block is the one that counts.
			open = false
			last = block
		case open:
			block = append(block, line)
		}
	}
	if open {
		return nil, "the report opens a findings block and never closes it"
	}
	if last == nil {
		return nil, ""
	}

	// Strict decode: an unknown field is refused rather than ignored, because
	// a findings list a reader half-understands is one that silently loses
	// the half it did not.
	var raw []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.Join(last, "\n"))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Sprintf("the findings block is not a JSON array: %v", err)
	}
	if dec.More() {
		return nil, "the findings block carries trailing content after the array"
	}
	out := []Finding{}
	for i, item := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, fmt.Sprintf("findings[%d] is not an object: %v", i, err)
		}
		for _, name := range findingFields {
			if _, ok := fields[name]; !ok {
				return nil, fmt.Sprintf("findings[%d] omits %q; every field is required, empty included", i, name)
			}
		}
		for name := range fields {
			if !knownFindingField[name] {
				return nil, fmt.Sprintf("findings[%d] carries %q, which is not a finding field", i, name)
			}
		}
		var finding Finding
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&finding); err != nil {
			return nil, fmt.Sprintf("findings[%d]: %v", i, err)
		}
		if err := finding.Validate(); err != nil {
			return nil, err.Error()
		}
		out = append(out, finding)
	}
	if len(out) == 0 {
		return nil, ""
	}
	return out, ""
}

// FindingsAsAny is the typed list as the open role payload carries it: a JSON
// round trip into []map[string]any, so what the envelope says is what the
// record is, field for field.
func FindingsAsAny(findings []Finding) []map[string]any {
	out := []map[string]any{}
	for _, finding := range findings {
		raw, err := json.Marshal(finding)
		if err != nil {
			continue
		}
		var as map[string]any
		if err := json.Unmarshal(raw, &as); err != nil {
			continue
		}
		out = append(out, as)
	}
	return out
}
