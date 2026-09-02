package parity

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// contracts/tracker-layout.json — EXECUTABLE.
//
// The tracker's on-disk record layout: where a record lives, which fields the
// control plane writes, the id alphabet, and the trace-id form. ticfac reaches
// the tracker through `tk --json` and never through these files (SPEC §3.1) —
// but contracts/README.md's `hosts` note is why this reader exists anyway: a
// host that CANNOT run tk reimplements the tracker's contract in its own
// language, and this file is what it reimplements against. ticfac is the
// consumer that will one day have such a host (SPEC Phase 4), so the layout is
// read here rather than taken on trust.
//
// The executable parts are the ones expressible as data: the record path from
// a tick id, the trace-id pattern against its own example, and the id
// alphabet against the ids the bundle's own examples use.

const trackerLayoutFile = "tracker-layout.json"

type trackerLayout struct {
	RecordDir         string `json:"record_dir"`
	RecordPathExample struct {
		Tick string `json:"tick"`
		Path string `json:"path"`
	} `json:"record_path_example"`
	Fields                 map[string]string `json:"fields"`
	EpicType               string            `json:"epic_type"`
	ParentOmittedWhenEmpty bool              `json:"parent_omitted_when_empty"`
	ControlPlane           struct {
		RequiredFields              []string `json:"required_fields"`
		DefaultStatus               string   `json:"default_status"`
		DefaultType                 string   `json:"default_type"`
		DefaultPriority             int      `json:"default_priority"`
		IDAlphabet                  string   `json:"id_alphabet"`
		IDMinLength                 int      `json:"id_min_length"`
		IDMaxLength                 int      `json:"id_max_length"`
		IDAttemptsPerLength         int      `json:"id_attempts_per_length"`
		JSONIndent                  int      `json:"json_indent"`
		ExternalRefSeparator        string   `json:"external_ref_separator"`
		ExternalRefOmittedWhenEmpty bool     `json:"external_ref_omitted_when_empty"`
	} `json:"written_by_the_control_plane"`
	TraceID struct {
		Prefix           string `json:"prefix"`
		HexLength        int    `json:"hex_length"`
		Pattern          string `json:"pattern"`
		Example          string `json:"example"`
		OmittedWhenEmpty bool   `json:"omitted_when_empty"`
	} `json:"trace_id"`
	HumanGate struct {
		Statuses        []string `json:"statuses"`
		CommittedStatus string   `json:"committed_status"`
		RejectedStatus  string   `json:"rejected_status"`
	} `json:"human_gate"`
}

// recordPath is ticfac's implementation of "where does tick <id> live".
func recordPath(l trackerLayout, tick string) string {
	return fmt.Sprintf("%s/%s.json", strings.TrimSuffix(l.RecordDir, "/"), tick)
}

func TestTrackerRecordPathIsDerivedFromTheTickID(t *testing.T) {
	var l trackerLayout
	readContract(t, trackerLayoutFile, &l)

	if l.RecordDir == "" {
		t.Fatal("the contract names no record directory")
	}
	got := recordPath(l, l.RecordPathExample.Tick)
	if got != l.RecordPathExample.Path {
		t.Errorf("record path for %q is %q, contract says %q", l.RecordPathExample.Tick, got, l.RecordPathExample.Path)
	}
}

// The trace-id form, from three directions that must agree: the prefix, the
// hex length, and the pattern — checked against the contract's own example and
// against a value the parts say should be legal.
func TestTraceIDPatternPrefixAndLengthAgree(t *testing.T) {
	var l trackerLayout
	readContract(t, trackerLayoutFile, &l)

	if l.TraceID.Pattern == "" || l.TraceID.Prefix == "" || l.TraceID.HexLength <= 0 {
		t.Fatalf("the trace id is not fully pinned: %+v", l.TraceID)
	}
	pattern, err := regexp.Compile(l.TraceID.Pattern)
	if err != nil {
		t.Fatalf("the pinned trace-id pattern does not compile in Go: %v", err)
	}
	if !pattern.MatchString(l.TraceID.Example) {
		t.Errorf("the contract's own example %q does not match its pattern %s", l.TraceID.Example, l.TraceID.Pattern)
	}
	if !strings.HasPrefix(l.TraceID.Example, l.TraceID.Prefix) {
		t.Errorf("the example %q does not carry the prefix %q", l.TraceID.Example, l.TraceID.Prefix)
	}
	if hex := strings.TrimPrefix(l.TraceID.Example, l.TraceID.Prefix); len(hex) != l.TraceID.HexLength {
		t.Errorf("the example carries %d hex characters, the contract says %d", len(hex), l.TraceID.HexLength)
	}

	// Built from the parts rather than copied from the example: a pattern that
	// stopped agreeing with prefix+hex_length would otherwise pass.
	built := l.TraceID.Prefix + strings.Repeat("a", l.TraceID.HexLength)
	if !pattern.MatchString(built) {
		t.Errorf("a trace id built from prefix and hex_length (%q) does not match the pattern", built)
	}
	tooShort := l.TraceID.Prefix + strings.Repeat("a", l.TraceID.HexLength-1)
	if pattern.MatchString(tooShort) {
		t.Errorf("the pattern accepts a short trace id %q", tooShort)
	}
	uppercase := l.TraceID.Prefix + strings.Repeat("A", l.TraceID.HexLength)
	if pattern.MatchString(uppercase) {
		t.Errorf("the pattern accepts upper-case hex %q; two spellings of one id is two ids", uppercase)
	}
}

// The id alphabet and its bounds, executable against the ids the bundle itself
// uses. A tick id in another contract that this alphabet would refuse means
// two files disagree about what an id is.
func TestTickIDAlphabetAdmitsTheIDsTheBundleUses(t *testing.T) {
	var l trackerLayout
	readContract(t, trackerLayoutFile, &l)

	cp := l.ControlPlane
	if cp.IDAlphabet == "" || cp.IDMinLength <= 0 || cp.IDMaxLength < cp.IDMinLength {
		t.Fatalf("the id rule is incoherent: %+v", cp)
	}
	if cp.IDAttemptsPerLength <= 0 {
		t.Error("id_attempts_per_length must be positive, or id minting never widens")
	}

	legal := func(id string) bool {
		if len(id) < cp.IDMinLength || len(id) > cp.IDMaxLength {
			return false
		}
		return strings.Trim(id, cp.IDAlphabet) == ""
	}

	for _, id := range []string{
		l.RecordPathExample.Tick, // this contract's own example
	} {
		if !legal(id) {
			t.Errorf("%q is used as a tick id in the bundle and this alphabet refuses it", id)
		}
	}
	for _, id := range []string{"", "a", "TAP", "tap-1", "toolong"} {
		if legal(id) {
			t.Errorf("%q is not a legal tick id and the rule accepts it", id)
		}
	}
}

// The fields the layout names are the fields a record carries, and the control
// plane's required set is a subset of nothing this file invents: every default
// names a field, and the human gate's statuses are closed.
func TestLayoutFieldsAndDefaultsAreCoherent(t *testing.T) {
	var l trackerLayout
	readContract(t, trackerLayoutFile, &l)

	if len(l.Fields) == 0 {
		t.Fatal("the contract names no fields")
	}
	for key, name := range l.Fields {
		if name == "" {
			t.Errorf("field %q maps to an empty name", key)
		}
	}
	required := map[string]bool{}
	for _, name := range l.ControlPlane.RequiredFields {
		if required[name] {
			t.Errorf("required field %q is listed twice", name)
		}
		required[name] = true
	}
	for _, name := range []string{l.Fields["id"], l.Fields["type"]} {
		if !required[name] {
			t.Errorf("%q is a named field and not required; a record without it cannot be addressed", name)
		}
	}
	if l.ControlPlane.DefaultStatus == "" || l.ControlPlane.DefaultType == "" {
		t.Error("the control plane writes defaults it does not name")
	}
	if l.ControlPlane.JSONIndent <= 0 {
		t.Error("json_indent must be positive, or every record write is a whole-file diff")
	}

	statuses := map[string]bool{}
	for _, s := range l.HumanGate.Statuses {
		statuses[s] = true
	}
	if !statuses[l.HumanGate.CommittedStatus] {
		t.Errorf("the human gate commits to %q, which is not one of its statuses %v",
			l.HumanGate.CommittedStatus, l.HumanGate.Statuses)
	}
	if statuses[l.HumanGate.RejectedStatus] {
		t.Errorf("the rejected status %q is also a committed status; the gate would not be a gate",
			l.HumanGate.RejectedStatus)
	}
	if !statuses[l.ControlPlane.DefaultStatus] {
		t.Errorf("the default status %q is not one of the tracker's statuses %v",
			l.ControlPlane.DefaultStatus, l.HumanGate.Statuses)
	}
	if l.EpicType == "" || l.EpicType == l.ControlPlane.DefaultType {
		t.Errorf("epic_type %q must be a distinct type from the default %q",
			l.EpicType, l.ControlPlane.DefaultType)
	}
}
