package wirevocab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The LIVE half of the contract: diff every schema-enumerated category
// against what herdr itself says, in both directions, so additions are as
// loud as removals. It runs whenever a herdr binary is on PATH — including
// under the full `make test` gate — and skips cleanly when there is none,
// because the binary's absence is not drift.
//
// WHAT THE OFFLINE RUN PROVES — stated plainly, per the logged decision of
// tick ic0 (finding 3): THE EPIC GATE DOES NOT REQUIRE A LIVE HERDR. The
// gate is the PR's CI run, and CI is a public-repo GitHub runner with no
// herdr to install — herdr is the operator's host tool, not a published
// artifact — so the gate cannot see this test do anything but skip. When
// herdr is absent, NOTHING ABOUT DRIFT IS PROVEN: the offline run proves
// only that the PINNED contract is internally consistent (contract_test.go)
// and that the client's and the fake's constants match the pin
// member-for-member (their vocabulary tests). It cannot notice the pin
// going stale against a herdr release nobody diffed — the pin's freshness
// rests entirely on this test RUNNING wherever a herdr exists: the
// operator's machine and worker hosts, where `make test` and
// `make test-short` execute it on every suite.
//
// `herdr api schema --json` answers with no server running, so this test
// needs no live session and stays cheap enough for every run.
//
// Error codes are deliberately NOT diffed here: error_response's code is an
// open string in the schema, so the live server cannot enumerate them. That
// category is pinned by observation in the contract, with provenance per
// member in error_codes.notes.

func needHerdr(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skipf("no herdr binary on PATH — live vocabulary drift is UNPROVEN by this run; " +
			"contract_test.go still cross-validates the PINNED snapshot, but nothing here says it matches a live herdr")
	}
}

func herdrJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	cmd := exec.Command("herdr", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("herdr %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("herdr %s: output is not JSON: %v", strings.Join(args, " "), err)
	}
	return doc
}

// drift reports the members present on only one side, so a failure reads as
// a vocabulary diff rather than a bare inequality.
func drift(contract, live []string) (onlyContract, onlyLive []string) {
	c := make(map[string]bool, len(contract))
	for _, m := range contract {
		c[m] = true
	}
	l := make(map[string]bool, len(live))
	for _, m := range live {
		l[m] = true
	}
	for _, m := range contract {
		if !l[m] {
			onlyContract = append(onlyContract, m)
		}
	}
	for _, m := range live {
		if !c[m] {
			onlyLive = append(onlyLive, m)
		}
	}
	return
}

func assertDiff(t *testing.T, pin pinCheck, name string, contract, live []string) {
	t.Helper()
	onlyContract, onlyLive := drift(contract, live)
	if len(onlyContract) == 0 && len(onlyLive) == 0 {
		return
	}
	if pin.mismatch != "" {
		t.Errorf("herdr VERSION MISMATCH, not necessarily a vocabulary bug: %s. The difference in %s: only in contract: %v; only in live herdr: %v",
			pin.mismatch, name, onlyContract, onlyLive)
		return
	}
	t.Errorf("wire vocabulary drift in %s within the pinned release (herdr %s) — bump internal/herd/wirevocab/herd-vocabulary.json deliberately: only in contract: %v; only in live herdr: %v",
		name, pin.pinned, onlyContract, onlyLive)
}

// pinCheck compares the installed herdr against the release the contract
// names (source.herdr_version, source.protocol). A drift on a host whose
// herdr differs from the pin is a version mismatch first: the fix is a
// deliberate re-snapshot against the new release (or installing the pinned
// one), and the failure says so instead of reading as a broken contract.
type pinCheck struct {
	pinned   string
	mismatch string
}

func checkPin(t *testing.T, v *Vocabulary, liveProtocol int) pinCheck {
	t.Helper()
	pin := pinCheck{pinned: v.Source.HerdrVersion}
	installed := installedHerdrVersion(t)
	var diffs []string
	if installed != v.Source.HerdrVersion {
		diffs = append(diffs, fmt.Sprintf("installed herdr %s, contract diffed against herdr %s", installed, v.Source.HerdrVersion))
	}
	if liveProtocol > 0 && liveProtocol != v.Source.Protocol {
		diffs = append(diffs, fmt.Sprintf("installed protocol %d, contract protocol %d", liveProtocol, v.Source.Protocol))
	}
	if len(diffs) > 0 {
		pin.mismatch = strings.Join(diffs, "; ") +
			" — re-snapshot herd-vocabulary.json against the installed herdr (update source.herdr_version, source.protocol and the members), or install the pinned release"
		t.Logf("herdr version mismatch: %s", pin.mismatch)
	}
	return pin
}

// installedHerdrVersion is the version `herdr --version` prints ("herdr
// 0.9.3" -> "0.9.3"), or the raw output when it has another shape.
func installedHerdrVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("herdr", "--version").Output()
	if err != nil {
		return "unknown (herdr --version: " + err.Error() + ")"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 2 && fields[0] == "herdr" {
		return fields[1]
	}
	return strings.TrimSpace(string(out))
}

func assertNoDupes(t *testing.T, name string, live []string) {
	t.Helper()
	seen := make(map[string]bool, len(live))
	for _, m := range live {
		if seen[m] {
			t.Errorf("live herdr %s repeats %q", name, m)
		}
		seen[m] = true
	}
}

// liveConsts walks oneOf[...] and pulls the const out of properties[key].
func liveConsts(t *testing.T, oneOf any, key, name string) []string {
	t.Helper()
	variants, ok := oneOf.([]any)
	if !ok {
		t.Fatalf("live schema: %s is not a oneOf", name)
	}
	var out []string
	for _, v := range variants {
		props, _ := v.(map[string]any)["properties"].(map[string]any)
		prop, _ := props[key].(map[string]any)
		c, _ := prop["const"].(string)
		if c == "" {
			t.Fatalf("live schema: %s variant without const %s", name, key)
		}
		out = append(out, c)
	}
	return out
}

func liveEnum(t *testing.T, node any, name string) []string {
	t.Helper()
	defs, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("live schema: %s is not an object", name)
	}
	e, ok := defs["enum"].([]any)
	if !ok {
		t.Fatalf("live schema: %s has no enum", name)
	}
	var out []string
	for _, m := range e {
		s, _ := m.(string)
		if s == "" {
			t.Fatalf("live schema: %s enum has a non-string member", name)
		}
		out = append(out, s)
	}
	return out
}

func TestLiveVocabularyMatchesContract(t *testing.T) {
	needHerdr(t)
	v := MustLoad()
	doc := herdrJSON(t, "api", "schema", "--json")

	liveProtocol, _ := doc["protocol"].(float64)
	pin := checkPin(t, v, int(liveProtocol))

	schemas, ok := doc["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("live schema: no schemas object")
	}
	request := schemaOf(t, schemas, "request")
	success := schemaOf(t, schemas, "success_response")
	event := schemaOf(t, schemas, "event")
	subEvent := schemaOf(t, schemas, "subscription_event")

	// Methods and result discriminators, from the request/success envelopes.
	liveMethods := liveConsts(t, request["oneOf"], "method", "request.oneOf")
	assertNoDupes(t, "methods", liveMethods)
	assertDiff(t, pin, "methods", v.Methods.Members, liveMethods)

	responseResult := defsOf(t, success, "success_response", "ResponseResult")
	liveResults := liveConsts(t, responseResult["oneOf"], "type", "ResponseResult.oneOf")
	assertNoDupes(t, "result_discriminators", liveResults)
	assertDiff(t, pin, "result_discriminators", v.ResultDiscriminators.Members, liveResults)

	// Both event-kind spellings.
	liveEventKinds := liveEnum(t, defsOf(t, event, "event", "EventKind"), "EventKind")
	assertDiff(t, pin, "event_kinds", v.EventKinds.Members, liveEventKinds)

	liveSubEventKinds := liveEnum(t, defsOf(t, subEvent, "subscription_event", "SubscriptionEventKind"), "SubscriptionEventKind")
	assertDiff(t, pin, "subscription_event_kinds", v.SubscriptionEventKinds.Members, liveSubEventKinds)

	// Subscription types, with pane scoping derived from the variants whose
	// required names pane_id — the same rule the client validates before
	// dialling, so the drift test must observe it on the live side too.
	subDefs := defsOf(t, request, "request", "Subscription")
	liveSubs := liveConsts(t, subDefs["oneOf"], "type", "Subscription.oneOf")
	assertNoDupes(t, "subscription_types", liveSubs)
	assertDiff(t, pin, "subscription_types", v.SubscriptionTypes.Members, liveSubs)

	var livePaneScoped []string
	for _, variant := range subDefs["oneOf"].([]any) {
		req, _ := variant.(map[string]any)["required"].([]any)
		for _, r := range req {
			if r == "pane_id" {
				props, _ := variant.(map[string]any)["properties"].(map[string]any)
				typ, _ := props["type"].(map[string]any)
				name, _ := typ["const"].(string)
				if name == "" {
					t.Fatalf("live schema: pane-scoped Subscription variant without const type")
				}
				livePaneScoped = append(livePaneScoped, name)
			}
		}
	}
	assertDiff(t, pin, "subscription_types.pane_scoped", v.SubscriptionTypes.PaneScoped, livePaneScoped)

	// Status words.
	liveStatuses := liveEnum(t, defsOf(t, request, "request", "AgentStatus"), "AgentStatus")
	assertDiff(t, pin, "agent_statuses", v.AgentStatuses.Members, liveStatuses)

	livePaneStates := liveEnum(t, defsOf(t, request, "request", "PaneAgentState"), "PaneAgentState")
	assertDiff(t, pin, "pane_agent_states", v.PaneAgentStates.Members, livePaneStates)

	// The small enums the client speaks.
	for _, tc := range []struct {
		name string
		def  string
		set  WordSet
	}{
		{"read_sources", "ReadSource", v.ReadSources},
		{"read_formats", "ReadFormat", v.ReadFormats},
		{"output_match_types", "OutputMatch", v.OutputMatchTypes},
		{"notification_sounds", "NotificationShowSound", v.NotificationSounds},
		{"toast_positions", "ToastHerdrPosition", v.ToastPositions},
	} {
		if tc.name == "output_match_types" {
			live := liveConsts(t, defsOf(t, request, "request", "OutputMatch")["oneOf"], "type", "OutputMatch.oneOf")
			assertDiff(t, pin, tc.name, tc.set.Members, live)
			continue
		}
		assertDiff(t, pin, tc.name, tc.set.Members,
			liveEnum(t, defsOf(t, request, "request", tc.def), tc.def))
	}

	// Metadata pins live: token name pattern, token cap, and the ttl_ms
	// bounds (one millisecond to one day; zero is not a value).
	params := defsOf(t, request, "request", "PaneReportMetadataParams")
	tokens, _ := params["properties"].(map[string]any)["tokens"].(map[string]any)
	if pattern, _ := tokens["propertyNames"].(map[string]any)["pattern"].(string); pattern != v.Metadata.TokenNamePattern {
		t.Errorf("metadata.token_name_pattern: contract %q, live %q", v.Metadata.TokenNamePattern, pattern)
	}
	if max, _ := tokens["maxProperties"].(float64); int(max) != v.Metadata.MaxTokens {
		t.Errorf("metadata.max_tokens: contract %d, live %d", v.Metadata.MaxTokens, int(max))
	}
	ttl, _ := params["properties"].(map[string]any)["ttl_ms"].(map[string]any)
	if min, _ := ttl["minimum"].(float64); int(min) != v.Metadata.TTLmsMin {
		t.Errorf("metadata.ttl_ms_min: contract %d, live %d", v.Metadata.TTLmsMin, int(min))
	}
	if max, _ := ttl["maximum"].(float64); int(max) != v.Metadata.TTLmsMax {
		t.Errorf("metadata.ttl_ms_max: contract %d, live %d", v.Metadata.TTLmsMax, int(max))
	}
}

func TestLiveAgentKindsMatchContract(t *testing.T) {
	needHerdr(t)
	v := MustLoad()
	pin := checkPin(t, v, 0)

	cmd := exec.Command("herdr", "agent", "start", "--help")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("herdr agent start --help: %v\n%s", err, stderr.String())
	}
	re := regexp.MustCompile(`\[possible values: ([^\]]+)\]`)
	m := re.FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("herdr agent start --help: no [possible values: ...] list in output:\n%s", stdout.String())
	}
	var live []string
	for _, kind := range strings.Split(m[1], ",") {
		live = append(live, strings.TrimSpace(kind))
	}
	assertDiff(t, pin, "agent_kinds", v.AgentKinds.Members, live)
}

// schemaOf fetches schemas.<name> from the live dump.
func schemaOf(t *testing.T, schemas map[string]any, name string) map[string]any {
	t.Helper()
	node, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("live schema: no schemas.%s", name)
	}
	return node
}

// defsOf fetches schemas.<schema>.$defs.<def>.
func defsOf(t *testing.T, schema map[string]any, schemaName, def string) map[string]any {
	t.Helper()
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("live schema: schemas.%s has no $defs", schemaName)
	}
	node, ok := defs[def].(map[string]any)
	if !ok {
		t.Fatalf("live schema: schemas.%s has no $def %s", schemaName, def)
	}
	return node
}
