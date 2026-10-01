package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
)

// The tracker-edit proposal (hn6 run_d51a, tick yjq).
//
// A worker never writes the tracker: `.tick/` is a protected prefix and tk is
// not a worker's to run, because the tracker is the run's authority and a
// record a worker wrote is a record nobody reviewed. That boundary is right —
// and it left one kind of tick no worker could ever finish: a tick whose
// DELIVERABLE is a tracker edit. yjq's was a one-field re-flow of epic hn6's
// acceptance_criteria (six [A<n>] marks on one line, so the parser read one
// item and A2–A6 were unaddressable). Three attempts answered BLOCKED with the
// exact text in prose, and the run redispatched it up the ladder into the
// same wall.
//
// So a worker PROPOSES the edit, typed, in its report, and the run — the
// tracker's own writer — validates it and applies it as the attempt's
// delivery, gated like any other:
//
//	```tracker-edits
//	[
//	  {"tick": "hn6", "field": "acceptance_criteria", "value": "[A1] …\n[A2] …"}
//	]
//	```
//
// The fields a worker may propose are the PROSE of a record: its acceptance
// criteria and its description (the value replaces the old, whole) and its
// notes (the value is appended as one note). Status, owner, parent, the
// graph's edges and every other field are the run's own and are refused here,
// at the report, where the worker can still fix its proposal in-session. A
// finding whose whole fix is a tracker edit carries the same object as its
// `tracker_edit` key (findings_v2.go), and the run applies it rather than
// filing a tick for it.
//
// What this reader checks is the SHAPE, context-free — collect has no
// checkout to read records from. The report checker adds what the checkout
// knows (the tick exists, an acceptance edit keeps every item the record
// marks), and the run holds the edit to the tracker as it stands when it
// applies it (internal/reconcile/tracker_edits.go).

// The fields a tracker edit may name.
const (
	TrackerFieldAcceptance  = "acceptance_criteria"
	TrackerFieldDescription = "description"
	TrackerFieldNotes       = "notes"
)

// TrackerEditFields is the closed field vocabulary, in the order messages
// name it.
var TrackerEditFields = []string{TrackerFieldAcceptance, TrackerFieldDescription, TrackerFieldNotes}

// runOwnedTrackerFields are fields a worker might reach for that are the
// run's alone — named so the refusal says WHY rather than "unknown field".
var runOwnedTrackerFields = map[string]bool{
	"status": true, "owner": true, "parent": true, "blocked_by": true, "after": true, "type": true,
	"priority": true, "labels": true, "role": true, "manual": true, "awaiting": true, "verdict": true,
	"closed_at": true, "closed_reason": true, "defer_until": true, "external_ref": true, "id": true,
	"title": true, "discovered_from": true, "created_by": true, "created_at": true, "updated_at": true,
	"started_at": true, "trace_id": true, "base_branch": true, "requires": true, "target_date": true,
	"gloss": true, "action": true,
}

// MaxTrackerEdits bounds one report's proposals: a tick that rewrites more
// records than this is not a tracker fix, it is a plan.
const MaxTrackerEdits = 8

// MaxTrackerEditValue bounds one value, in bytes.
const MaxTrackerEditValue = 16384

// trackerEditTick is a tick id a record path can be built from: no
// separators, no dots.
var trackerEditTick = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// TrackerEdit is one proposed change to one field of one tracker record.
type TrackerEdit struct {
	Tick  string `json:"tick"`
	Field string `json:"field"`
	Value string `json:"value"`
}

// String names the edit for a record or a message.
func (e TrackerEdit) String() string {
	return fmt.Sprintf("%s of %s", e.Field, e.Tick)
}

// Validate is the edit's context-free shape: a tick id, a field a worker may
// propose, and a value the tracker can hold.
func (e TrackerEdit) Validate() error {
	if strings.TrimSpace(e.Tick) == "" {
		return fmt.Errorf("names no tick")
	}
	if !trackerEditTick.MatchString(e.Tick) {
		return fmt.Errorf("%q is not a tick id", e.Tick)
	}
	switch {
	case runOwnedTrackerFields[e.Field]:
		return fmt.Errorf("%q is the run's own field, never a worker's to propose; a worker proposes %s",
			e.Field, fieldList())
	case !oneOf(TrackerEditFields, e.Field):
		return fmt.Errorf("field %q is not one a worker may propose; the field is %s", e.Field, fieldList())
	}
	if strings.TrimSpace(e.Value) == "" {
		return fmt.Errorf("the value is empty; an edit states the new %s in full", e.Field)
	}
	if len(e.Value) > MaxTrackerEditValue {
		return fmt.Errorf("the value is %d bytes; at most %d", len(e.Value), MaxTrackerEditValue)
	}
	for _, r := range e.Value {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7f {
			return fmt.Errorf("the value carries a control character (%U); prose only", r)
		}
	}
	if e.Field == TrackerFieldAcceptance {
		if _, err := acceptance.Parse(e.Value); err != nil {
			return fmt.Errorf("the new acceptance criteria do not parse: %v", err)
		}
	}
	return nil
}

func fieldList() string {
	return strings.Join(TrackerEditFields[:len(TrackerEditFields)-1], ", ") + " or " +
		TrackerEditFields[len(TrackerEditFields)-1]
}

// TrackerEditsBlock is what a report's tracker-edits block says.
type TrackerEditsBlock struct {
	// Present is whether the report carries a complete block.
	Present bool
	// Line is the report line the block opens on.
	Line  int
	Edits []TrackerEdit
	// Problem is why the block cannot be applied as written; Edits is nil
	// whenever it is set — a proposal is applied whole or not at all.
	Problem string
}

var trackerEditsFence = regexp.MustCompile("^```tracker-edits(?:[ \\t]+v1)?[ \\t]*$")

// ReadTrackerEditsBlock reads the report's tracker-edits block. The FINAL
// complete block wins, for the reason the final findings block does.
func ReadTrackerEditsBlock(body string) TrackerEditsBlock {
	var last, block []string
	lastLine, openLine := 0, 0
	open := false
	for i, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case !open:
			if trackerEditsFence.MatchString(line) {
				open, block, openLine = true, nil, i+1
			}
		case anyFence.MatchString(line):
			open = false
			last, lastLine = block, openLine
			if last == nil {
				last = []string{}
			}
		default:
			block = append(block, line)
		}
	}
	if open {
		return TrackerEditsBlock{Line: openLine,
			Problem: fmt.Sprintf("the report opens a tracker-edits block (line %d) and never closes it", openLine)}
	}
	if last == nil {
		return TrackerEditsBlock{}
	}
	out := TrackerEditsBlock{Present: true, Line: lastLine}
	text := strings.Join(last, "\n")
	var raw []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	if err := dec.Decode(&raw); err != nil {
		out.Problem = fmt.Sprintf("the tracker-edits block%s is not a JSON array: %v", blockLineAt(text, lastLine, err), err)
		return out
	}
	if dec.More() {
		out.Problem = "the tracker-edits block carries trailing content after the array"
		return out
	}
	switch {
	case len(raw) == 0:
		out.Problem = "the tracker-edits block proposes no edit; leave the block out when there is nothing to change"
		return out
	case len(raw) > MaxTrackerEdits:
		out.Problem = fmt.Sprintf("the tracker-edits block proposes %d edits; at most %d", len(raw), MaxTrackerEdits)
		return out
	}
	edits := make([]TrackerEdit, 0, len(raw))
	for i, item := range raw {
		edit, err := readTrackerEdit(item)
		if err != nil {
			out.Problem = fmt.Sprintf("tracker-edits[%d] %v", i, err)
			return out
		}
		edits = append(edits, edit)
	}
	out.Edits = edits
	return out
}

// readTrackerEdit decodes one edit object strictly — an edit is a WRITE, so
// a key this reader does not know is refused rather than folded — and holds
// it to Validate.
func readTrackerEdit(raw json.RawMessage) (TrackerEdit, error) {
	var edit TrackerEdit
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return edit, fmt.Errorf("is null; an edit is an object {\"tick\", \"field\", \"value\"}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&edit); err != nil {
		return edit, fmt.Errorf("is not an object {\"tick\", \"field\", \"value\"}: %v", err)
	}
	if err := edit.Validate(); err != nil {
		return edit, err
	}
	return edit, nil
}

// ------------------------------------------------------ the checker's half ---

// lintTrackerEdits holds a report's proposals to what the checkout knows: the
// tick each names is a record of this tracker, and an acceptance edit keeps
// every [A<n>] item the record marks — an edit may re-flow or add to a
// definition of done, never drop from it. Errors here are pushed back, never
// fatal: collect has no checkout, and the run holds the edit to the tracker
// as it stands when it applies it. where names a lone edit (a finding's);
// empty, each edit is named by its place in the report's block.
func (out *LintResult) lintTrackerEdits(where string, edits []TrackerEdit, repo string) {
	if repo == "" {
		return
	}
	for i, edit := range edits {
		at := where
		if at == "" {
			at = fmt.Sprintf("tracker-edits[%d]", i)
		}
		if msg := TrackerEditAgainst(edit, repo); msg != "" {
			out.Errors = append(out.Errors, LintProblem{Where: at, Message: msg})
		}
	}
}

// TrackerEditAgainst is the checkout's answer about one edit: "" when the
// record exists and the edit keeps what it must, else why not.
func TrackerEditAgainst(edit TrackerEdit, repo string) string {
	rec, ok := readTickRecord(repo, edit.Tick)
	if !ok {
		return fmt.Sprintf("%q is not a tick this checkout's tracker carries (.tick/issues/%s.json); name the record "+
			"the edit is for", edit.Tick, edit.Tick)
	}
	if edit.Field == TrackerFieldAcceptance {
		if missing := DroppedAcceptanceItems(rec.AcceptanceCriteria, edit.Value); len(missing) > 0 {
			return fmt.Sprintf("the new acceptance criteria of %s drop %s, which the record marks: an edit may re-flow "+
				"or add to a definition of done, never drop an item from it", edit.Tick, strings.Join(missing, ", "))
		}
	}
	return ""
}

// markedItem finds every [A<n>] mark in a text, wherever it stands: a record
// whose marks share one line (yjq's) still MEANS those items.
var markedItem = regexp.MustCompile(`\[(A[1-9][0-9]{0,2})\]`)

// DroppedAcceptanceItems is every [A<n>] item the old criteria mark — at a
// line's start or not — that the new criteria do not carry as an item.
func DroppedAcceptanceItems(old, updated string) []string {
	items, err := acceptance.Parse(updated)
	if err != nil {
		return nil // the shape check answers for a value that does not parse
	}
	have := map[string]bool{}
	for _, item := range items {
		have[item.ID] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, m := range markedItem.FindAllStringSubmatch(old, -1) {
		id := m[1]
		if !have[id] && !seen[id] {
			missing = append(missing, id)
		}
		seen[id] = true
	}
	return missing
}
