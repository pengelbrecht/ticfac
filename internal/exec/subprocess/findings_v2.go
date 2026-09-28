package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// Findings format v2 (tick 4m6).
//
// v1 was a record shaped for the READER — the envelope's five pinned fields
// plus two evidence fields bolted on by tick nfo — and workers got it wrong in
// the same few ways, live:
//
//   - kind values outside the vocabulary (hence #61's normalisation), and a
//     kind, `upstream-tick`, that said WHERE a finding goes — the target's job
//     — and then required the target anyway;
//   - `demonstrating_check` filled with "none", "go" or prose, and `done_item`
//     spelled apart from it, so half a claim could be made;
//   - five fields required "empty included", so a finding with nothing to say
//     about its target still had to write `"target": ""`;
//   - titles too long to triage.
//
// v2 is shaped for the WRITER, and the reader maps it onto the same internal
// record (Finding), so everything downstream — the envelope's pinned
// $defs.finding, the draft, routing, the absorption decision — is unchanged:
//
//	```findings v2
//	[
//	  {
//	    "kind": "defect",                        // defect | proposal | contract-change
//	    "title": "≤ 80 characters",
//	    "severity": "medium",                    // low | medium | high
//	    "body": "optional prose",
//	    "target": "owner/name",                  // optional; omitted = this repository
//	    "breaks": {"item": "A3", "check": "go"}, // optional; check optional
//	    "evidence": "internal/x/y.go:42 — the failing line"  // optional
//	  }
//	]
//	```
//
//   - kind says what the finding IS. Routing is target's alone: a proposal
//     with a target is v1's upstream-tick, without one v1's proposed-tick;
//     contract-change is v1's contract; defect is defect.
//   - Only kind, title and severity are required. body and target are
//     optional, and an omitted target means this repository.
//   - breaks is ONE optional claim: the acceptance item of the epic the
//     finding breaks, and optionally the check that demonstrates it — a
//     [testing.commands] / [evidence.commands] id or a runnable command. A
//     finding that breaks nothing omits breaks; there is no "none". It maps
//     onto done_item / demonstrating_check.
//   - evidence is where to look (file:line, or a command and the line it
//     fails with), folded into the body as a labelled line so it rides the
//     draft to triage without a new pinned field.
//   - `[]` is an explicit "no findings", distinguishable from a report that
//     carries no block (FindingsBlock.Present).
//
// The fence names the version (```findings v2). An untagged block is read as
// v2 when its items carry a v2-only key or kind, so a worker that forgets the
// tag is not refused for it; otherwise it is v1, which is read forever
// (existing runs and archived reports).

// The v2 kind vocabulary.
const (
	FindingV2KindDefect         = "defect"
	FindingV2KindProposal       = "proposal"
	FindingV2KindContractChange = "contract-change"
)

// FindingV2Kinds is the closed v2 kind vocabulary.
var FindingV2Kinds = []string{FindingV2KindDefect, FindingV2KindProposal, FindingV2KindContractChange}

// MaxFindingTitle is tk's long-title rule: a title is one line a person can
// triage from a list.
const MaxFindingTitle = 80

// knownV2Field is the closed key set of a v2 item.
var knownV2Field = map[string]bool{
	"kind": true, "title": true, "body": true, "severity": true, "target": true, "breaks": true, "evidence": true,
}

// V2FindingFieldNames is the v2 key set, sorted, for messages.
func V2FindingFieldNames() []string {
	names := make([]string, 0, len(knownV2Field))
	for name := range knownV2Field {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// v2Required are the keys a v2 item must carry.
var v2Required = []string{"kind", "title", "severity"}

// noClaim are the spellings of "nothing" workers put in a claim field. In v2
// the way to claim nothing is to omit the field.
var noClaim = map[string]bool{"": true, "none": true, "n/a": true, "na": true, "-": true, "null": true}

// looksLikeV2 reports whether an untagged block's items are v2-shaped: any
// v2-only key, or any v2-only kind.
func looksLikeV2(items []map[string]json.RawMessage) bool {
	for _, fields := range items {
		if _, ok := fields["breaks"]; ok {
			return true
		}
		if _, ok := fields["evidence"]; ok {
			return true
		}
		var kind string
		if json.Unmarshal(fields["kind"], &kind) == nil &&
			(kind == FindingV2KindProposal || kind == FindingV2KindContractChange) {
			return true
		}
	}
	return false
}

type findingV2 struct {
	Kind     string          `json:"kind"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Severity string          `json:"severity"`
	Target   string          `json:"target"`
	Breaks   json.RawMessage `json:"breaks"`
	Evidence string          `json:"evidence"`
}

// readFindingV2 reads one v2 item and maps it onto the internal record. It
// refuses only what the record can mean nothing by — a required key missing,
// a value of the wrong JSON type, an empty title (Validate) — and repairs the
// rest the way v1 does, noting each repair.
func readFindingV2(i int, fields map[string]json.RawMessage) (Finding, []FindingNote, string) {
	for _, name := range v2Required {
		if _, ok := fields[name]; !ok {
			return Finding{}, nil, fmt.Sprintf("findings[%d] omits %q; a v2 finding requires %s",
				i, name, strings.Join(v2Required, ", "))
		}
	}
	unknown := make([]string, 0)
	known := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		if knownV2Field[name] {
			known[name] = value
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	filtered, err := json.Marshal(known)
	if err != nil {
		return Finding{}, nil, fmt.Sprintf("findings[%d] could not be read: %v", i, err)
	}
	var item findingV2
	dec := json.NewDecoder(bytes.NewReader(filtered))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&item); err != nil {
		return Finding{}, nil, fmt.Sprintf("findings[%d]: %v", i, err)
	}

	finding := Finding{Title: item.Title, Body: item.Body, Severity: item.Severity, Target: item.Target}
	var notes []FindingNote
	for _, name := range unknown {
		finding.Body = foldIntoBody(finding.Body, name, fields[name])
		why := "a v2 finding has no such field (known: " + strings.Join(V2FindingFieldNames(), ", ") + ")"
		switch name {
		case "done_item", "demonstrating_check":
			why = "v2 states the done evidence as \"breaks\": {\"item\": \"A<n>\", \"check\": \"<command id>\"}"
		}
		notes = append(notes, FindingNote{Index: i, Key: name, Original: foldValue(fields[name]), Why: why})
	}
	normalise := func(key, value, why string) {
		raw, _ := json.Marshal(value)
		finding.Body = foldIntoBody(finding.Body, key, raw)
		notes = append(notes, FindingNote{Index: i, Key: key, Original: value, Normalised: true, Why: why})
	}

	// kind: what it IS, in the v2 vocabulary; outside it, a defect.
	kind := item.Kind
	if !oneOf(FindingV2Kinds, kind) {
		normalise("kind", kind, "kind is one of "+strings.Join(FindingV2Kinds, ", ")+
			" (where it goes is target's job)")
		kind = FindingV2KindDefect
	}
	if !oneOf(FindingSeverities, finding.Severity) {
		normalise("severity", finding.Severity, "severity is one of "+strings.Join(FindingSeverities, ", "))
		finding.Severity = FindingSeverityMedium
	}
	if finding.Target != "" && !targetRepositoryPattern.MatchString(finding.Target) {
		original := finding.Target
		finding.Target = ""
		if m := targetPrefixPattern.FindStringSubmatch(strings.TrimSpace(original)); m != nil {
			finding.Target = m[1]
		}
		normalise("target", original, "target is an owner/name repository, e.g. pengelbrecht/ticks; omit it for this repository")
	}
	// Routing is the target's: the kind maps onto the record's vocabulary
	// only once the target is settled.
	switch kind {
	case FindingV2KindProposal:
		finding.Kind = FindingKindProposedTick
		if finding.Target != "" {
			finding.Kind = FindingKindUpstreamTick
		}
	case FindingV2KindContractChange:
		finding.Kind = FindingKindContract
	default:
		finding.Kind = FindingKindDefect
	}

	if len(item.Breaks) > 0 && string(item.Breaks) != "null" {
		breaksNotes, doneItem, check := readBreaks(i, item.Breaks)
		for _, note := range breaksNotes {
			finding.Body = foldIntoBody(finding.Body, note.Key, json.RawMessage(jsonString(note.Original)))
			notes = append(notes, note)
		}
		finding.DoneItem, finding.DemonstratingCheck = doneItem, check
	}
	if evidence := strings.TrimSpace(item.Evidence); evidence != "" {
		finding.Body = appendBodyLine(finding.Body, "evidence: "+evidence)
	}
	return finding, notes, ""
}

// readBreaks reads a v2 `breaks` claim. A claim that cannot be keyed to an
// acceptance item is dropped from the record with the original folded into
// the body — the worker's words are kept, the record carries no link it
// cannot follow — and noted, so the linter asks for it to be fixed.
func readBreaks(i int, raw json.RawMessage) (notes []FindingNote, doneItem, check string) {
	note := func(key, original, why string) {
		notes = append(notes, FindingNote{Index: i, Key: key, Original: original, Normalised: true, Why: why})
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		note("breaks", foldValue(raw), `breaks is an object, {"item": "A<n>", "check": "<command id>"}; omit it when the finding breaks no item`)
		return notes, "", ""
	}
	var item, chk string
	for key, value := range fields {
		switch key {
		case "item", "check":
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				note("breaks."+key, foldValue(value), "breaks."+key+" is a string")
				continue
			}
			if key == "item" {
				item = strings.TrimSpace(s)
			} else {
				chk = strings.TrimSpace(s)
			}
		default:
			note("breaks."+key, foldValue(value), `breaks carries only "item" and "check"`)
		}
	}
	sort.Slice(notes, func(a, b int) bool { return notes[a].Key < notes[b].Key })
	if !runconfig.AcceptanceItemPattern.MatchString(item) {
		note("breaks.item", item, "breaks.item is an acceptance item id of the epic, like A3 ("+
			runconfig.AcceptanceItemPattern.String()+"); omit breaks when the finding breaks no item")
		if chk != "" {
			note("breaks.check", chk, "a check without an item links nothing")
		}
		return notes, "", ""
	}
	if _, hasCheck := fields["check"]; hasCheck && noClaim[strings.ToLower(chk)] {
		note("breaks.check", chk, "breaks.check names a [testing.commands] id or a runnable command; omit it rather than write \"none\"")
		chk = ""
	}
	return notes, item, chk
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// appendBodyLine adds one line to a finding's body.
func appendBodyLine(body, line string) string {
	if body == "" {
		return line
	}
	return body + "\n" + line
}
