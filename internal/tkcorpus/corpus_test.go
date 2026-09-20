package tkcorpus

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The pinned corpus, relative to this package's directory: a fixture of the
// TypeScript suite (cloudflare/test/tk-write-corpus.test.ts imports it), so it
// lives where that suite can import it from, and this package reaches it by
// path. Never hand-edit it; regenerate it with -update.
const pinPath = "../../cloudflare/test/fixtures/tk-write-corpus.json"

var update = flag.Bool("update", false, "regenerate the pinned corpus from the tk on PATH instead of comparing against it")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

// ------------------------------------------------------------------ schema --

type corpusFile struct {
	Generated corpusHeader `json:"generated"`
	Cases     []corpusCase `json:"cases"`
}

type corpusHeader struct {
	By            string   `json:"by"`
	Regenerate    string   `json:"regenerate"`
	From          string   `json:"from"`
	Canonicalized []string `json:"canonicalized"`
	TZ            string   `json:"tz"`
	TkVersion     string   `json:"tk_version"`
	HandEdited    string   `json:"hand_edited"`
}

type corpusCase struct {
	ID     string   `json:"id"`
	Op     corpusOp `json:"op"`
	Now    string   `json:"now"`
	Result string   `json:"result"` // "written" | "refused"
	// Before is JSON null for a create — the client under test has no create
	// (creates belong to tracker-write.ts) — and the exact bytes the record
	// held when the op started for every other kind.
	Before *string `json:"before"`
	After  string  `json:"after"`
}

type corpusOp struct {
	Kind string `json:"kind"` // create | claim | note | update | close | reopen
	// Argv is the tk command line with ids already canonical: the corpus is
	// readable as "this is the tk command that produced this record".
	Argv []string `json:"argv"`
	// The typed fields the TypeScript replay maps onto the client's calls.
	Owner  string  `json:"owner,omitempty"`
	Text   string  `json:"text,omitempty"`
	From   string  `json:"from,omitempty"`
	Notes  *string `json:"notes,omitempty"`
	Reason *string `json:"reason,omitempty"`
}

// -------------------------------------------------------------------- specs --

// The canonical clock. create stamps every record at the create instant; each
// op runs at its own instant, five minutes after the last, so every note line
// the corpus pins lands on its own minute. Millisecond precision is the
// contract's own rendering: it is what a JavaScript Date renders, so the
// TypeScript replay can reproduce the pinned bytes exactly.
var (
	createInstant = time.Date(2026, 9, 20, 11, 0, 0, 0, time.UTC)
	opBaseInstant = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opStep        = 5 * time.Minute
)

func instant(ms time.Time) string {
	return ms.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// createSpecs is the generated corpus of records: every field tk's `create`
// can set, with the byte shapes the two serializers can disagree about —
// HTML escaping, quotes, backslashes, tabs, control bytes, unicode —
// deliberately present. `{gNN}` references an earlier tick's id.
type createSpec struct {
	id   string
	argv []string
}

var createSpecs = []createSpec{
	{"g01", []string{"create", "An epic for the corpus", "-t", "epic", "-p", "1", "--json"}},
	{"g02", []string{"create", "Plain task", "--json"}},
	{"g03", []string{"create", "Zero priority corner", "-p", "0", "--json"}},
	{"g04", []string{"create", "Rich record", "-d",
		"Desc with & < > and \"quotes\" plus backslash \\ plus caf\u00e9 and a ctrl-\u0001 and a separator \u2028 end",
		"-l", "api,beta", "-p", "4", "-t", "feature", "-o", "team@example.com",
		"-b", "{g02}", "-b", "{g03}", "--after", "{g02}", "--parent", "{g01}", "--json"}},
	{"g05", []string{"create", "Gated work", "--requires", "approval",
		"-d", "gate: the provider choice needs a human", "--parent", "{g01}", "--json"}},
	{"g06", []string{"create", "Awaiting checkpoint", "--awaiting", "checkpoint", "--parent", "{g01}", "--json"}},
	{"g07", []string{"create", "Final review", "--role", "review", "-b", "{g02}", "-p", "3",
		"-d", "gate: review is a human gate", "--parent", "{g01}", "--json"}},
	{"g08", []string{"create", "Deferred and targeted", "--defer", "2027-03-01",
		"--target-date", "2026-12-31", "--external-ref", "gh-42", "--parent", "{g01}", "--json"}},
	{"g09", []string{"create", "Discovered and accepted", "--discovered-from", "{g02}",
		"--acceptance", "The bytes match & the test fails on drift", "--parent", "{g01}", "--json"}},
	{"g10", []string{"create", "Owned and labeled", "-o", "crew@example.com",
		"-l", "solo", "-t", "chore", "--parent", "{g01}", "--json"}},
}

// opSpecs is the write corpus: every controlled write the TypeScript client
// performs, in the order the pin replays them, each with the result tk gave.
type opSpec struct {
	id        string // canonical id
	kind      string // claim | note | update | close | reopen
	result    string // written | refused
	routed    bool   // a refused close tk routes to awaiting (the record still changes)
	owner     string
	text      string
	from      string
	notes     string
	reason    string
	hasReason bool
}

var opSpecs = []opSpec{
	{id: "g02", kind: "claim", result: "written", owner: "worker@example.com"},
	// Re-claim: tk keeps the first started_at — the stale-recovery invariant,
	// byte-pinned.
	{id: "g02", kind: "claim", result: "written", owner: "second@example.com"},
	{id: "g02", kind: "note", result: "written", text: "PR ready: merged & deployed <prod>", from: "agent"},
	{id: "g02", kind: "note", result: "written", text: "  operator direction: less < more  ", from: "human"},
	{id: "g02", kind: "close", result: "written", reason: "done & done", hasReason: true},
	{id: "g02", kind: "reopen", result: "written"},
	// Claim after close: a fresh started_at, closed fields cleared.
	{id: "g02", kind: "claim", result: "written", owner: "back@example.com"},
	{id: "g03", kind: "claim", result: "written", owner: "worker@example.com"},
	{id: "g03", kind: "update", result: "written", notes: "line one\nline two & <tag>\nthird with separator \u2028 end"},
	{id: "g03", kind: "update", result: "written", notes: ""},
	{id: "g03", kind: "close", result: "written"},
	{id: "g05", kind: "claim", result: "written", owner: "worker@example.com"},
	// A gated close: tk refuses and routes the tick to awaiting, writing the
	// routing record — a refusal with a commit.
	{id: "g05", kind: "close", result: "refused", routed: true, reason: "work complete", hasReason: true},
	// A plain close of an awaiting tick: the awaiting field survives the close.
	{id: "g06", kind: "close", result: "written"},
	{id: "g08", kind: "note", result: "written", text: "text with tab\tand café <ok>", from: "agent"},
	{id: "g08", kind: "close", result: "written", reason: "shipped & closed", hasReason: true},
	{id: "g09", kind: "note", result: "written", text: "first note", from: "agent"},
	{id: "g09", kind: "note", result: "written", text: "second & final <sep \u2028>", from: "agent"},
	// An epic with open children: tk refuses and commits nothing.
	{id: "g01", kind: "close", result: "refused"},
	{id: "g07", kind: "claim", result: "written", owner: "worker@example.com"},
	{id: "g07", kind: "close", result: "written", reason: "review held", hasReason: true},
	{id: "g07", kind: "reopen", result: "written"},
	{id: "g04", kind: "close", result: "written", reason: "child done", hasReason: true},
	{id: "g04", kind: "reopen", result: "written"},
	{id: "g10", kind: "close", result: "written"},
}

func (s opSpec) argv() []string {
	id := "{id}"
	switch s.kind {
	case "claim":
		return []string{"update", id, "--status", "in_progress", "--owner", s.owner, "--json"}
	case "note":
		return []string{"note", id, s.text, "--from", s.from, "--json"}
	case "update":
		return []string{"update", id, "--notes", s.notes, "--json"}
	case "close":
		if s.hasReason {
			return []string{"close", id, "--reason", s.reason, "--json"}
		}
		return []string{"close", id, "--json"}
	case "reopen":
		return []string{"reopen", id, "--json"}
	}
	return nil
}

// ---------------------------------------------------------------- generator --

type caseRules struct {
	now        string // the op's canonical instant
	setStarted bool   // the op writes started_at fresh
	setClosed  bool   // the op writes closed_at fresh
	// noteRest is the Go-escaped text after the `YYYY-MM-DD HH:MM - ` stamp of
	// the line this op appends; empty when it appends none.
	noteRest string
}

func generateCorpus(t *testing.T, tkPath string) []byte {
	t.Helper()

	dir := t.TempDir()
	gitInit(t, dir)
	runTk(t, tkPath, dir, "init", 0)

	cases := []corpusCase{}
	// canonical -> real id, and the reverse for argv substitution.
	realOf := map[string]string{}
	// real id / clock value -> canonical, assigned deterministically by the
	// script: same op order, same mapping, every regeneration.
	substs := map[string]string{}
	// A note line as tk wrote it (real stamp and all) -> the canonical line,
	// registered at the op that appended it. Lines persist in `notes` across
	// later ops, so every canonicalization pass must rewrite every line ever
	// appended, not just the current op's — the raw file keeps the real stamps
	// forever.
	lineSubsts := map[string]string{}
	// The normalized bytes and raw bytes each record held after its last op.
	lastNorm := map[string]string{}
	lastRaw := map[string]string{}

	for _, spec := range createSpecs {
		argv := make([]string, len(spec.argv))
		for i, arg := range spec.argv {
			argv[i] = substituteIDs(arg, realOf)
		}
		stdout, code := runTkCode(t, tkPath, dir, argv, "create "+spec.id)
		if code != 0 {
			t.Fatalf("tk %s: exit %d\n%s", strings.Join(argv, " "), code, stdout)
		}
		var created map[string]any
		if err := json.Unmarshal([]byte(stdout), &created); err != nil {
			t.Fatalf("tk %s: stdout is not the created record: %v\n%s", strings.Join(argv, " "), err, stdout)
		}
		realID, ok := created["id"].(string)
		if !ok || realID == "" {
			t.Fatalf("tk %s: no id in the created record", strings.Join(argv, " "))
		}
		realOf[spec.id] = realID
		raw := readRecord(t, dir, realID)
		after := canonicalizeRecord(t, raw, realOf, substs, lineSubsts, spec.id, caseRules{now: instant(createInstant)})
		lastNorm[spec.id] = after
		lastRaw[spec.id] = raw
		cases = append(cases, corpusCase{
			ID:     spec.id,
			Op:     corpusOp{Kind: "create", Argv: canonicalArgv(spec.argv, spec.id)},
			Now:    instant(createInstant),
			Result: "written",
			Before: nil,
			After:  after,
		})
	}

	for j, spec := range opSpecs {
		now := instant(opBaseInstant.Add(time.Duration(j) * opStep))
		rawBefore := lastRaw[spec.id]
		before := lastNorm[spec.id]
		if rawBefore == "" {
			t.Fatalf("op on %s before any create", spec.id)
		}
		argv := make([]string, 0, 8)
		for _, arg := range spec.argv() {
			argv = append(argv, strings.ReplaceAll(arg, "{id}", realOf[spec.id]))
		}
		_, code := runTkCode(t, tkPath, dir, argv, spec.kind+" "+spec.id)
		if spec.result == "written" && code != 0 {
			t.Fatalf("tk %s: expected exit 0, got %d", strings.Join(argv, " "), code)
		}
		if spec.result == "refused" && code == 0 {
			t.Fatalf("tk %s: expected a refusal, got exit 0", strings.Join(argv, " "))
		}

		op := corpusOp{Kind: spec.kind, Argv: canonicalArgv(spec.argv(), spec.id)}
		switch spec.kind {
		case "claim":
			op.Owner = spec.owner
		case "note":
			op.Text = spec.text
			op.From = spec.from
		case "update":
			op.Notes = &spec.notes
		case "close":
			if spec.hasReason {
				op.Reason = &spec.reason
			}
		}

		var after string
		if spec.result == "refused" && !spec.routed {
			// A refusal that commits nothing: the record must be untouched.
			rawAfter := readRecord(t, dir, realOf[spec.id])
			if rawAfter != rawBefore {
				t.Fatalf("tk %s: refused without routing, but the record changed", strings.Join(argv, " "))
			}
			after = before
		} else {
			rawAfter := readRecord(t, dir, realOf[spec.id])
			rules := caseRules{now: now}
			switch {
			case spec.kind == "claim" && statusOf(t, before) != "in_progress":
				rules.setStarted = true
			case spec.kind == "close" && spec.result == "written":
				rules.setClosed = true
			}
			if spec.kind == "note" {
				noteRest := escapedLine(strings.TrimSpace(spec.text))
				if spec.from == "human" {
					noteRest = "[human] " + noteRest
				}
				rules.noteRest = noteRest
			}
			if spec.routed {
				rules.noteRest = escapedLine("Work complete, awaiting " + requiresOf(t, before))
			}
			after = canonicalizeRecord(t, rawAfter, realOf, substs, lineSubsts, spec.id, rules)
			lastRaw[spec.id] = rawAfter
		}
		beforeCopy := before
		cases = append(cases, corpusCase{
			ID:     spec.id,
			Op:     op,
			Now:    now,
			Result: spec.result,
			Before: &beforeCopy,
			After:  after,
		})
		lastNorm[spec.id] = after
	}

	version := runTkVersion(t, tkPath, dir)
	corpus := corpusFile{
		Generated: corpusHeader{
			By:         "internal/tkcorpus, from a real tk binary driven command by command",
			Regenerate: "go test ./internal/tkcorpus -update",
			From:       "the exact bytes tk committed to .tick/issues/ per write, canonicalized as below",
			Canonicalized: []string{
				"tick ids: the random ids tk minted, mapped to g01, g02, ... in creation order",
				"clock values: created_at to the create instant; updated_at, and started_at/closed_at when the op sets them, to the case's `now`; a kept started_at/closed_at keeps the canonical instant of the op that wrote it",
				"note-line stamps: the appended line's `YYYY-MM-DD HH:MM` to the `now` instant's UTC minute",
			},
			TZ:         "the tk subprocesses run with TZ=UTC (tk stamps note lines in local time; the host the TypeScript client serves is UTC)",
			TkVersion:  version,
			HandEdited: "this file is generated; regenerate it, never edit it",
		},
		Cases: cases,
	}
	encoded, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus: %v", err)
	}
	return append(encoded, '\n')
}

// substituteIDs replaces `{gNN}` placeholders with the real ids minted so far.
func substituteIDs(arg string, realOf map[string]string) string {
	for canonical, real := range realOf {
		arg = strings.ReplaceAll(arg, "{"+canonical+"}", real)
	}
	return arg
}

// canonicalArgv is the recorded command line: placeholders resolved to the
// canonical ids, so the pin reads as the tk command that produced the record.
func canonicalArgv(argv []string, canonical string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		out[i] = strings.ReplaceAll(arg, "{id}", canonical)
	}
	return out
}

func escapedLine(text string) string {
	// The exact bytes tk embedded in the record for this note text: what
	// Go's encoding/json produces, which is what tk writes it with.
	encoded, err := json.Marshal(text)
	if err != nil {
		return text
	}
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), "\""), "\"")
}

func statusOf(t *testing.T, record string) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(record), &parsed); err != nil {
		t.Fatalf("normalized record does not parse: %v", err)
	}
	status, _ := parsed["status"].(string)
	return status
}

func requiresOf(t *testing.T, record string) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(record), &parsed); err != nil {
		t.Fatalf("normalized record does not parse: %v", err)
	}
	requires, _ := parsed["requires"].(string)
	if requires == "" {
		t.Fatal("routed close on a record with no requires field")
	}
	return requires
}

// canonicalizeRecord rewrites tk's raw record bytes into the canonical form the
// pin carries: tk's own serialization — field order, omitempty, indent,
// escaping — with ids, clock values and note-line stamps substituted. Every
// substitution asserts the value it replaces had the shape tk writes, so a tk
// that changes the SHAPE fails the regeneration here rather than pinning
// something nobody can account for.
func canonicalizeRecord(t *testing.T, raw string, realOf, substs, lineSubsts map[string]string, canonicalID string, rules caseRules) string {
	t.Helper()

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("tk record does not parse: %v", err)
	}

	// Clock fields. created_at is seen first at the create, where it equals
	// the create instant; every later sight must resolve to that.
	created, _ := parsed["created_at"].(string)
	canonicalCreated, ok := substs[created]
	if !ok {
		canonicalCreated = instant(createInstant)
		substs[created] = canonicalCreated
	}
	updated, _ := parsed["updated_at"].(string)
	substs[updated] = rules.now

	out := raw
	out = replaceOnce(t, out, `"created_at": "`+created+`"`, `"created_at": "`+canonicalCreated+`"`)
	out = replaceOnce(t, out, `"updated_at": "`+updated+`"`, `"updated_at": "`+rules.now+`"`)

	if started, ok := parsed["started_at"].(string); ok {
		canonical := ""
		if rules.setStarted {
			canonical = rules.now
			substs[started] = canonical
		} else {
			var found bool
			canonical, found = substs[started]
			if !found {
				t.Fatalf("started_at %q was never set by a canonicalized op", started)
			}
		}
		out = replaceOnce(t, out, `"started_at": "`+started+`"`, `"started_at": "`+canonical+`"`)
	}
	if closed, ok := parsed["closed_at"].(string); ok {
		canonical := ""
		if rules.setClosed {
			canonical = rules.now
			substs[closed] = canonical
		} else {
			var found bool
			canonical, found = substs[closed]
			if !found {
				t.Fatalf("closed_at %q was never set by a canonicalized op", closed)
			}
		}
		out = replaceOnce(t, out, `"closed_at": "`+closed+`"`, `"closed_at": "`+canonical+`"`)
	}

	// Note lines. The line this op appended (if any) is registered in the
	// durable line table under its REAL stamp; then every line ever appended
	// is rewritten to its canonical stamp — tk keeps the real stamps in the
	// raw file across ops, and a pass that only fixed the current line would
	// let earlier lines revert to the real clock.
	if rules.noteRest != "" {
		stamp, err := time.Parse(time.RFC3339, updated)
		if err != nil {
			t.Fatalf("updated_at %q is not RFC3339", updated)
		}
		realLine := stamp.UTC().Format("2006-01-02 15:04") + " - " + rules.noteRest
		if _, seen := lineSubsts[realLine]; !seen {
			if !strings.Contains(out, realLine) {
				t.Fatalf("the appended note line is not %q — tk's line stamp and its updated_at straddled a minute boundary, or the line format changed; rerun the generation", realLine)
			}
			canonicalStamp, err := time.Parse(time.RFC3339, rules.now)
			if err != nil {
				t.Fatalf("canonical now %q is not RFC3339", rules.now)
			}
			lineSubsts[realLine] = canonicalStamp.UTC().Format("2006-01-02 15:04") + " - " + rules.noteRest
		}
	}
	for realLine, canonicalLine := range lineSubsts {
		out = strings.ReplaceAll(out, realLine, canonicalLine)
	}

	// Ids: every quoted real id becomes the quoted canonical one, through a
	// placeholder pass so a real id that happens to equal an already-assigned
	// canonical id cannot collide.
	for canonical, real := range realOf {
		out = strings.ReplaceAll(out, `"`+real+`"`, "\"\x00"+canonical+"\x00\"")
	}
	out = strings.ReplaceAll(out, "\x00", "")

	// The result must parse and be the record it claims to be.
	var final map[string]any
	if err := json.Unmarshal([]byte(out), &final); err != nil {
		t.Fatalf("canonicalized record does not parse: %v", err)
	}
	if id, _ := final["id"].(string); id != canonicalID {
		t.Fatalf("canonicalized record id is %q, want %q", id, canonicalID)
	}
	return out
}

func replaceOnce(t *testing.T, text, old, new string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("expected exactly one %q in the record, found %d", old, n)
	}
	return strings.Replace(text, old, new, 1)
}

// ------------------------------------------------------------------ tk runner --

func tkEnv() []string {
	return append(os.Environ(),
		"TZ=UTC",
		"TICK_OWNER=corpus@example.com",
		"TK_ACTOR=corpus-agent",
	)
}

func runTkCode(t *testing.T, tkPath, dir string, argv []string, what string) (string, int) {
	t.Helper()
	cmd := exec.Command(tkPath, argv...)
	cmd.Dir = dir
	cmd.Env = tkEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("%s: %v", what, err)
	}
	// A refusal is an answer, not an error: the caller decides which exit
	// codes it expects.
	return stdout.String(), exitErr.ExitCode()
}

func runTk(t *testing.T, tkPath, dir string, subcommand string, wantCode int) {
	t.Helper()
	_, code := runTkCode(t, tkPath, dir, []string{subcommand}, subcommand)
	if code != wantCode {
		t.Fatalf("tk %s: expected exit %d, got %d", subcommand, wantCode, code)
	}
}

func runTkVersion(t *testing.T, tkPath, dir string) string {
	t.Helper()
	cmd := exec.Command(tkPath, "version")
	cmd.Dir = dir
	cmd.Env = tkEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tk version: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func readRecord(t *testing.T, dir, id string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".tick", "issues", id+".json"))
	if err != nil {
		t.Fatalf("read record %s: %v", id, err)
	}
	return string(raw)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "tkcorpus generator")
	run("config", "user.email", "corpus@example.com")
	run("remote", "add", "origin", "https://example.com/example/tkcorpus.git")
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("corpus repository\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	run("add", "fixture.txt")
	run("commit", "-q", "-m", "corpus repository")
}

// --------------------------------------------------------------------- tests --

// TestTkWriteCorpusMatchesCurrentTk is the guard on the tk side: it rebuilds
// the corpus with the tk on PATH and byte-compares against the pin. A tk that
// changes what it writes — field order, omitempty, escaping, note lines, the
// close and claim semantics, a new field — fails here, on every host that has
// tk, which is every host the per-tick gate runs on. A CI runner without tk
// skips it, exactly as internal/tk's real-binary test does.
func TestTkWriteCorpusMatchesCurrentTk(t *testing.T) {
	tkPath, err := exec.LookPath("tk")
	if err != nil {
		t.Skip("tk binary not on PATH; skipping the tk write-corpus guard")
	}

	generated := generateCorpus(t, tkPath)

	if *update {
		if err := os.WriteFile(pinPath, generated, 0o644); err != nil {
			t.Fatalf("write the pinned corpus: %v", err)
		}
		t.Logf("regenerated %s from the tk on PATH", pinPath)
		return
	}

	pinned, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatalf("the pinned corpus is missing: %v\nregenerate it with: go test ./internal/tkcorpus -update", err)
	}
	if string(generated) == string(pinned) {
		return
	}
	reportCorpusDrift(t, string(pinned), string(generated))
}

// reportCorpusDrift names the first case that differs, so the failure says
// WHICH write changed rather than that 34 cases differ somewhere.
func reportCorpusDrift(t *testing.T, pinned, generated string) {
	t.Helper()
	var pin, regen corpusFile
	if err := json.Unmarshal([]byte(pinned), &pin); err != nil {
		t.Fatalf("the pinned corpus does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(generated), &regen); err != nil {
		t.Fatalf("the regenerated corpus does not parse: %v", err)
	}
	if pin.Generated.TkVersion != regen.Generated.TkVersion {
		t.Fatalf("the corpus was generated by a different tk:\n  pinned: %s\n  current: %s\nregenerate deliberately with: go test ./internal/tkcorpus -update", pin.Generated.TkVersion, regen.Generated.TkVersion)
	}
	if len(pin.Cases) != len(regen.Cases) {
		t.Fatalf("the corpus changed shape: %d pinned cases, %d regenerated", len(pin.Cases), len(regen.Cases))
	}
	for i := range pin.Cases {
		p := pin.Cases[i]
		g := regen.Cases[i]
		if p.ID != g.ID || p.Op.Kind != g.Op.Kind {
			t.Fatalf("case %d changed: pinned %s/%s, regenerated %s/%s", i, p.ID, p.Op.Kind, g.ID, g.Op.Kind)
		}
		pb, gb := "", ""
		if p.Before != nil {
			pb = *p.Before
		}
		if g.Before != nil {
			gb = *g.Before
		}
		if pb != gb {
			t.Fatalf("case %s (%s): the BEFORE bytes tk wrote changed\npinned:\n%s\nregenerated:\n%s", p.ID, p.Op.Kind, pb, gb)
		}
		if p.After != g.After {
			t.Fatalf("case %s (%s): the AFTER bytes tk wrote changed\npinned:\n%s\nregenerated:\n%s\nregenerate deliberately with: go test ./internal/tkcorpus -update", p.ID, p.Op.Kind, p.After, g.After)
		}
	}
	t.Fatalf("the corpora differ outside their cases; regenerate deliberately with: go test ./internal/tkcorpus -update")
}

// TestTkWriteCorpusPinIsWellFormed is the no-tk half of the guard: it reads the
// pin and refuses a corpus that is empty, unchained or trimmed to the cases
// that happen to pass — the failure shape this tick exists to remove. It runs
// everywhere, including a CI runner without tk.
func TestTkWriteCorpusPinIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatalf("the pinned corpus is missing: %v\nregenerate it with: go test ./internal/tkcorpus -update (needs tk on PATH)", err)
	}
	var corpus corpusFile
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("the pinned corpus does not parse: %v", err)
	}

	idShape := regexp.MustCompile(`^g\d\d$`)
	lastAfter := map[string]string{}
	kinds := map[string]bool{}
	escapedAmpersand := false
	refused := false
	routedRefusal := false
	unchangedRefusal := false
	humanNote := false

	for _, c := range corpus.Cases {
		if !idShape.MatchString(c.ID) {
			t.Fatalf("case id %q is not a canonical corpus id", c.ID)
		}
		if !map[string]bool{"create": true, "claim": true, "note": true, "update": true, "close": true, "reopen": true}[c.Op.Kind] {
			t.Fatalf("case %s: unknown op kind %q", c.ID, c.Op.Kind)
		}
		kinds[c.Op.Kind] = true

		var record map[string]any
		if err := json.Unmarshal([]byte(c.After), &record); err != nil {
			t.Fatalf("case %s: the after bytes are not a tick record: %v", c.ID, err)
		}
		if id, _ := record["id"].(string); id != c.ID {
			t.Fatalf("case %s: the after bytes carry id %q", c.ID, id)
		}

		if c.Op.Kind == "create" {
			if c.Before != nil {
				t.Fatalf("case %s: a create must have no before", c.ID)
			}
			lastAfter[c.ID] = c.After
			continue
		}
		if c.Before == nil {
			t.Fatalf("case %s: only a create may have no before", c.ID)
		}
		if c.Before != nil && *c.Before != lastAfter[c.ID] {
			t.Fatalf("case %s (%s): the before bytes are not the previous case's after — the corpus is not a chain", c.ID, c.Op.Kind)
		}
		if strings.Contains(c.After, `\u0026`) || strings.Contains(c.After, `\u003c`) {
			escapedAmpersand = true
		}
		if c.Result == "refused" {
			refused = true
			if c.After != *c.Before {
				routedRefusal = true
			} else {
				unchangedRefusal = true
			}
		}
		if c.Op.Kind == "note" && c.Op.From == "human" {
			humanNote = true
		}
		lastAfter[c.ID] = c.After
	}

	if len(corpus.Cases) < 30 {
		t.Fatalf("the corpus has %d cases; a trimmed corpus is a hand-picked one — regenerate it whole with: go test ./internal/tkcorpus -update", len(corpus.Cases))
	}
	for _, kind := range []string{"claim", "note", "update", "close", "reopen"} {
		if !kinds[kind] {
			t.Fatalf("the corpus has no %s case", kind)
		}
	}
	if !escapedAmpersand {
		t.Fatal("no case pins Go's HTML escaping (\\u0026 / \\u003c) — the exact divergence this corpus exists to catch")
	}
	if !refused || !routedRefusal || !unchangedRefusal {
		t.Fatal("the corpus must pin both refusals: a routed close (bytes change) and a plain refusal (bytes unchanged)")
	}
	if !humanNote {
		t.Fatal("the corpus has no --from human note — the provenance boundary is unpinned")
	}
}
