package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The triage surface (tick sg5): the everyday half of the findings channel
// that `ticfac finding` kept behind a 64-hex key, a --by on every call, and a
// tick the operator had to create by hand before the command could record the
// promotion. What has to hold:
//
//   - every untriaged finding is settled WITHOUT the key: by an interactive
//     walk that shows kind, severity, title, body and the discovering tick, or
//     by a short key prefix a scripted agent passes on the command line;
//   - absorb CREATES the tick under the epic — an open child of the epic is
//     what blocks the close-out (3h0's gate) — and file creates a backlog tick
//     with no parent, owned by the actor;
//   - the actor defaults from git config, and a checkout that names nobody is
//     a refusal naming --by, not a silent "the operator";
//   - --json round-trips: the listing an agent decides from is JSON, and so
//     is the report of what each decision did;
//   - a decision is never made twice, and the routed finding is refused the
//     promotions that would drop its routing.

// triageKey is a finding key in the shape the channel really files them: the
// 64-hex dedup key no person should ever have to type.
func triageKey(short string) string {
	return strings.Repeat(short, 8)
}

// gitIn runs one git command in a checkout of the test's own, for the config
// the actor default reads and for reading records off the branch.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// showOnOrigin reads one path off the integration branch as ORIGIN holds it:
// the durable half of every record these tests assert, because a record a
// branch does not carry is a record the next wave's worker cannot read.
func showOnOrigin(t *testing.T, repo, path string) string {
	t.Helper()
	origin := strings.TrimSpace(gitIn(t, repo, "remote", "get-url", "origin"))
	return gitIn(t, origin, "show", "refs/heads/epic/qeu:"+path)
}

func readBack(t *testing.T, repo string) *runstate.Store {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	return store
}

// The --json listing is the half of the round-trip an agent starts from:
// everything the interactive walk shows, and nothing but JSON on stdout, so
// the loop reads without parsing prose.
func TestTriageJSONListsTheUntriagedForAnAgent(t *testing.T) {
	repo := newFindingsRepo(t)
	local := testDraftFinding(triageKey("d34db33f"), "")
	local.Body = "The body an agent must read before deciding."
	local.DoneItem, local.DemonstratingCheck = "A2", "go"
	seedFinding(t, repo, local)
	seedFinding(t, repo, testDraftFinding(triageKey("c0ffee00"), "pengelbrecht/ticks"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--json", "--repo", repo, "qeu"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("the --json listing does not round-trip: %v\n%s", err, stdout.String())
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d drafts, want the 2 untriaged:\n%s", len(listed), stdout.String())
	}
	byKey := map[string]map[string]any{}
	for _, f := range listed {
		byKey[f["key"].(string)] = f
	}
	localJSON := byKey[triageKey("d34db33f")]
	if localJSON == nil {
		t.Fatalf("the listing does not carry the local draft:\n%s", stdout.String())
	}
	for field, want := range map[string]string{
		"kind":                "proposed-tick",
		"severity":            "high",
		"title":               "A finding the surface lists",
		"body":                "The body an agent must read before deciding.",
		"discovered_from":     "run-epic-qeu/tick-a1/attempt-1",
		"tick_id":             "a1",
		"status":              "proposed",
		"linkage":             `breaks done item A2 (demonstrated by "go")`,
		"demonstrating_check": "go",
	} {
		if got := localJSON[field]; got != want {
			t.Errorf("the local draft's %s is %v, want %q", field, got, want)
		}
	}
	if got := byKey[triageKey("c0ffee00")]["target"]; got != "pengelbrecht/ticks" {
		t.Errorf("the routed draft's target is %v, want pengelbrecht/ticks", got)
	}
}

// Absorb is the decision that used to demand the most of the operator: the
// tick created by hand, then promoted by key. Here the command creates the
// tick itself — on the branch, under the epic, open — which is what blocks
// the close-out: the gate does not start while a child of the epic is open.
func TestTriageAbsorbCreatesTheTickUnderTheEpic(t *testing.T) {
	repo := newFindingsRepo(t)
	key := triageKey("d34db33f")
	seedFinding(t, repo, testDraftFinding(key, ""))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu", "d34=absorb"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("absorb exit %d: %s", code, stderr.String())
	}

	finding, ok, err := readBack(t, repo).Finding(key)
	if err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.TriagedBy != "the operator" {
		t.Fatalf("the draft is %s by %q, want promoted by the operator", finding.Status, finding.TriagedBy)
	}
	tickID := finding.PromotedAs
	if tickID == "" {
		t.Fatal("the promotion names no tick")
	}

	raw := showOnOrigin(t, repo, filepath.Join(".tick", "issues", tickID+".json"))
	var tick map[string]any
	if err := json.Unmarshal([]byte(raw), &tick); err != nil {
		t.Fatalf("the created record does not read back: %v\n%s", err, raw)
	}
	for field, want := range map[string]string{
		"parent":          "qeu",
		"status":          "open",
		"owner":           "ticfac",
		"created_by":      "the operator",
		"discovered_from": "run-epic-qeu/tick-a1/attempt-1",
		"title":           "A finding the surface lists",
	} {
		if got := tick[field]; got != want {
			t.Errorf("the created tick's %s is %v, want %q", field, got, want)
		}
	}

	out := stdout.String()
	for _, want := range []string{
		"absorbed into epic qeu as tick " + tickID,
		"the operator's decision",
		"close-out does not hand over",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}
}

// File settles a finding the done is reachable with: a backlog tick, no
// parent, owned by the person who filed it — nothing in the run waits on it.
func TestTriageFilesABacklogTickWithNoParent(t *testing.T) {
	repo := newFindingsRepo(t)
	key := triageKey("d34db33f")
	seedFinding(t, repo, testDraftFinding(key, ""))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu", "d34=file"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("file exit %d: %s", code, stderr.String())
	}

	finding, ok, err := readBack(t, repo).Finding(key)
	if err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted {
		t.Fatalf("the draft is %s, want promoted", finding.Status)
	}
	tickID := finding.PromotedAs
	raw := showOnOrigin(t, repo, filepath.Join(".tick", "issues", tickID+".json"))
	var tick map[string]any
	if err := json.Unmarshal([]byte(raw), &tick); err != nil {
		t.Fatalf("the created record does not read back: %v\n%s", err, raw)
	}
	if parent, ok := tick["parent"]; ok {
		t.Errorf("a backlog tick carries parent %v, want none", parent)
	}
	if got := tick["owner"]; got != "the operator" {
		t.Errorf("a backlog tick's owner is %v, want the actor", got)
	}
	if got := tick["status"]; got != "open" {
		t.Errorf("a backlog tick is %v, want open", got)
	}
	if !strings.Contains(stdout.String(), "backlog tick "+tickID) {
		t.Errorf("stdout does not name the filed tick:\n%s", stdout.String())
	}
}

// Discard and fixed are the two verdicts the old command already recorded —
// here settled by prefix, in one pass, without the 64-hex keys.
func TestTriageDiscardAndFixedByShortPrefix(t *testing.T) {
	repo := newFindingsRepo(t)
	localKey, routedKey := triageKey("d34db33f"), triageKey("c0ffee00")
	seedFinding(t, repo, testDraftFinding(localKey, ""))
	seedFinding(t, repo, testDraftFinding(routedKey, "pengelbrecht/ticks"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu",
		"d34=discard", "c0f=fixed:338bbf8b"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	store := readBack(t, repo)
	local, ok, err := store.Finding(localKey)
	if err != nil || !ok {
		t.Fatalf("read the local draft back: %v %v", ok, err)
	}
	if local.Status != runstate.FindingDiscarded || local.TriagedBy != "the operator" {
		t.Fatalf("the local draft is %s by %q, want discarded by the operator", local.Status, local.TriagedBy)
	}
	routed, ok, err := store.Finding(routedKey)
	if err != nil || !ok {
		t.Fatalf("read the routed draft back: %v %v", ok, err)
	}
	if routed.Status != runstate.FindingFixed || routed.FixedAs != "338bbf8b" {
		t.Fatalf("the routed draft is %s as %q, want fixed as 338bbf8b", routed.Status, routed.FixedAs)
	}
	out := stdout.String()
	if !strings.Contains(out, "discarded") || !strings.Contains(out, "is not suppressed") {
		t.Errorf("stdout does not report both verdicts:\n%s", out)
	}
}

// The --json decision report is the other half of the round-trip: what each
// decision did, machine-readable, with the tick an absorb created and the
// error a refusal carries.
func TestTriageJSONReportsWhatEachDecisionDid(t *testing.T) {
	repo := newFindingsRepo(t)
	absorbKey := triageKey("d34db33f")
	seedFinding(t, repo, testDraftFinding(absorbKey, ""))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--json", "--repo", repo, "--by", "the operator", "qeu",
		"d34=absorb"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("the --json report does not round-trip: %v\n%s", err, stdout.String())
	}
	if len(results) != 1 {
		t.Fatalf("reported %d decisions, want 1:\n%s", len(results), stdout.String())
	}
	result := results[0]
	if result["key"] != absorbKey || result["decision"] != "absorb" || result["by"] != "the operator" {
		t.Errorf("the result is %v, want the absorb of %s by the operator", result, absorbKey)
	}
	tickID, _ := result["tick"].(string)
	if tickID == "" {
		t.Fatalf("the result names no tick: %v", result)
	}
	raw := showOnOrigin(t, repo, filepath.Join(".tick", "issues", tickID+".json"))
	if !strings.Contains(raw, `"parent": "qeu"`) {
		t.Errorf("the reported tick is not under the epic:\n%s", raw)
	}
}

// The actor defaults from the identity the checkout already attributes the
// person's commits with — the name first, the email when no name is set — and
// a checkout that names nobody is a refusal naming --by, because a decision
// nobody can attribute is one nobody can audit.
func TestTheTriageActorDefaultsFromGitConfig(t *testing.T) {
	repo := newFindingsRepo(t)
	// The host's own global git config is kept out of the read for the whole
	// test, so the default is about THIS checkout's identity, whatever
	// machine runs this — and the refusal case below really is a checkout
	// that names nobody.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	gitIn(t, repo, "config", "user.name", "Ada Lovelace")
	key := triageKey("d34db33f")
	seedFinding(t, repo, testDraftFinding(key, ""))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "qeu", "d34=discard"}, &stdout, &stderr); code != 0 {
		t.Fatalf("named default exit %d: %s", code, stderr.String())
	}
	if finding, ok, err := readBack(t, repo).Finding(key); err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	} else if finding.TriagedBy != "Ada Lovelace" {
		t.Errorf("triaged_by %q, want the git config name", finding.TriagedBy)
	}

	// The email, when no name is set.
	gitIn(t, repo, "config", "--unset", "user.name")
	gitIn(t, repo, "config", "user.email", "ada@example.com")
	emailKey := triageKey("c0ffee00")
	seedFinding(t, repo, testDraftFinding(emailKey, ""))
	stdout.Reset()
	if code := Run([]string{"triage", "--repo", repo, "qeu", "c0f=discard"}, &stdout, &stderr); code != 0 {
		t.Fatalf("email default exit %d: %s", code, stderr.String())
	}
	if finding, ok, err := readBack(t, repo).Finding(emailKey); err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	} else if finding.TriagedBy != "ada@example.com" {
		t.Errorf("triaged_by %q, want the git config email", finding.TriagedBy)
	}

	// Neither: a refusal naming the flag, not a silent "the operator".
	gitIn(t, repo, "config", "--unset", "user.email")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"triage", "--repo", repo, "qeu", "9f2=discard"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("no-identity exit %d, want %d: %s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--by") {
		t.Errorf("stderr %q does not name --by", stderr.String())
	}
}

// The interactive walk: the person reads each finding — kind, severity,
// title, body, the discovering tick, the linkage — and decides in one word,
// never typing a key of any length.
func TestTriageWalksEachFindingInteractively(t *testing.T) {
	repo := newFindingsRepo(t)
	routedKey, localKey := triageKey("c0ffee00"), triageKey("d34db33f")
	routed := testDraftFinding(routedKey, "pengelbrecht/ticks")
	routed.Body = "A routed finding's body the walk must show."
	local := testDraftFinding(localKey, "")
	local.DoneItem, local.DemonstratingCheck = "A2", "go"
	seedFinding(t, repo, routed)
	seedFinding(t, repo, local)

	// The drafts walk in key order: the routed one first, the local one
	// second. The scripted person discards the routed finding — absorb and
	// file are not offered on a routing they would drop — and absorbs the
	// local one.
	old := triageStdin
	triageStdin = strings.NewReader("d\nabsorb\n")
	t.Cleanup(func() { triageStdin = old })
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu"}, &stdout, &stderr); code != 0 {
		t.Fatalf("walk exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"A finding the surface lists",
		"proposed-tick",
		"high",
		"A routed finding's body the walk must show.",
		"discovered by run-epic-qeu/tick-a1/attempt-1",
		`breaks done item A2 (demonstrated by "go")`,
		"unlinked: names no done item",
		"for pengelbrecht/ticks",
		"absorb",
		"decided",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}

	store := readBack(t, repo)
	first, ok, err := store.Finding(routedKey)
	if err != nil || !ok {
		t.Fatalf("read the routed draft back: %v %v", ok, err)
	}
	if first.Status != runstate.FindingDiscarded {
		t.Errorf("the routed draft is %s, want discarded", first.Status)
	}
	second, ok, err := store.Finding(localKey)
	if err != nil || !ok {
		t.Fatalf("read the local draft back: %v %v", ok, err)
	}
	if second.Status != runstate.FindingPromoted || second.PromotedAs == "" {
		t.Errorf("the local draft is %s as %q, want promoted", second.Status, second.PromotedAs)
	}
	raw := showOnOrigin(t, repo, filepath.Join(".tick", "issues", second.PromotedAs+".json"))
	if !strings.Contains(raw, `"parent": "qeu"`) {
		t.Errorf("the absorbed tick is not under the epic:\n%s", raw)
	}
}

// Skip and a closed stream are honest stops, not failures: the finding stays
// proposed, the walk says so, and the close-out keeps holding the hand-over —
// exactly the gate triage exists to clear, still up.
func TestTriageSkipsAndStopsHonestly(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding(triageKey("d34db33f"), ""))
	seedFinding(t, repo, testDraftFinding(triageKey("c0ffee00"), ""))

	old := triageStdin
	triageStdin = strings.NewReader("skip\n")
	t.Cleanup(func() { triageStdin = old })
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu"}, &stdout, &stderr); code != 0 {
		t.Fatalf("skip exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"still waiting", "2 still waiting for a person", "no more decisions to read"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}
	store := readBack(t, repo)
	for _, key := range []string{triageKey("d34db33f"), triageKey("c0ffee00")} {
		finding, ok, err := store.Finding(key)
		if err != nil || !ok {
			t.Fatalf("read %s back: %v %v", key, ok, err)
		}
		if finding.Status != runstate.FindingProposed {
			t.Errorf("%s is %s, want still proposed after a skip and a closed stream", key, finding.Status)
		}
	}
}

// What the surface refuses: a decision it cannot parse, a prefix that
// matches nothing or more than one draft, and the promotions that would drop
// a finding's routing. A malformed decision is a usage mistake; the rest are
// lookups and routings it honestly cannot address.
func TestTriageRefusesWhatItCannotSettle(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding(triageKey("d34db33f"), ""))
	seedFinding(t, repo, testDraftFinding(triageKey("d34db39e"), ""))
	seedFinding(t, repo, testDraftFinding(triageKey("c0ffee00"), "pengelbrecht/ticks"))

	var stdout, stderr bytes.Buffer
	for _, bad := range [][]string{
		{"qeu", "d34"},            // no verb
		{"qeu", "d34=explode"},    // not a decision
		{"qeu", "d34=fixed"},      // the scripted form is fixed:<commit>
		{"qeu", "d34=fixed:nope"}, // the fixed verdict names a commit id
	} {
		stderr.Reset()
		if code := Run(append([]string{"triage", "--repo", repo, "--by", "who"}, bad...),
			&stdout, &stderr); code != exitUsage {
			t.Errorf("the malformed decision %q exits %d, want %d: %s",
				strings.Join(bad, " "), code, exitUsage, stderr.String())
		}
	}

	// No epic id at all.
	stderr.Reset()
	if code := Run([]string{"triage", "--repo", repo}, &stdout, &stderr); code != exitUsage {
		t.Errorf("the invocation with no epic id exits %d, want %d", code, exitUsage)
	}

	// A prefix that matches nothing.
	stderr.Reset()
	if code := Run([]string{"triage", "--repo", repo, "--by", "who", "qeu", "zzz=discard"},
		&stdout, &stderr); code != exitGeneric {
		t.Errorf("the unknown prefix exits %d, want %d: %s", code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no findings draft starts with") {
		t.Errorf("stderr %q does not name what the prefix missed", stderr.String())
	}

	// A prefix that matches more than one draft.
	stderr.Reset()
	if code := Run([]string{"triage", "--repo", repo, "--by", "who", "qeu", "d34=discard"},
		&stdout, &stderr); code != exitGeneric {
		t.Errorf("the ambiguous prefix exits %d, want %d: %s", code, exitGeneric, stderr.String())
	}
	for _, key := range []string{triageKey("d34db33f"), triageKey("d34db39e")} {
		if !strings.Contains(stderr.String(), key) {
			t.Errorf("the ambiguous refusal does not name %s:\n%s", key, stderr.String())
		}
	}

	// The promotions that would drop a routing.
	stderr.Reset()
	if code := Run([]string{"triage", "--repo", repo, "--by", "who", "qeu", "c0f=absorb"},
		&stdout, &stderr); code != exitGeneric {
		t.Errorf("the routed absorb exits %d, want %d: %s", code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "routed to pengelbrecht/ticks") ||
		!strings.Contains(stderr.String(), "ticfac finding") {
		t.Errorf("the routed refusal does not name the routing and the command that respects it:\n%s", stderr.String())
	}
}

// A decision is never made twice: the second pass over a decided draft is not
// an error and not a decision either, and an absorb that arrives too late
// creates nothing.
func TestATriageDecisionIsNeverMadeTwice(t *testing.T) {
	repo := newFindingsRepo(t)
	key := triageKey("d34db33f")
	seedFinding(t, repo, testDraftFinding(key, ""))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"triage", "--repo", repo, "--by", "the operator", "qeu", "d34=discard"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("first decision exit %d: %s", code, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"triage", "--repo", repo, "--by", "someone else", "qeu", "d34=absorb"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("second decision exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already discarded") ||
		!strings.Contains(stdout.String(), "never made twice") {
		t.Errorf("stdout does not report the standing decision:\n%s", stdout.String())
	}
	if finding, ok, err := readBack(t, repo).Finding(key); err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	} else if finding.Status != runstate.FindingDiscarded || finding.TriagedBy != "the operator" {
		t.Errorf("the draft is %s by %q, want the operator's discard to stand",
			finding.Status, finding.TriagedBy)
	}
	// The late absorb created no tick.
	if out := gitIn(t, repo, "ls-remote", "--heads", "origin"); strings.Contains(out, "nope") {
		t.Fatal("unreachable")
	}
	origin := strings.TrimSpace(gitIn(t, repo, "remote", "get-url", "origin"))
	if tree := gitIn(t, origin, "ls-tree", "--name-only", "refs/heads/epic/qeu"); strings.Contains(tree, ".tick") {
		t.Errorf("a decided draft's late absorb created a tick anyway:\n%s", tree)
	}
}
