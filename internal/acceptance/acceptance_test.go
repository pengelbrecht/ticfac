package acceptance

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// factSheet is the shape ticks' goal design writes into a container's
// acceptance criteria: a goal paragraph, then one [A<n>]-marked line per
// discrete, testable fact (goal-design.md, "Where a goal lives"). The mark
// leads the line; ids are A<n> with n in 1..999 and no leading zeros.
const factSheet = `Goal: port sqlite to Rust, because maintenance cost tracks
the C it wraps and the port removes the last of it.

[A1] The sqlite storage layer is implemented in Rust; no C sources remain in storage/.
[A2] The full existing test suite passes unchanged against the Rust implementation.
[A3] Benchmark suite ` + "`bench/storage`" + ` shows a median latency improvement of 30% or more over the C baseline in bench/baseline.json.
[A4] (human judgment) Release notes read well and position the change accurately.`

// unmarkedProse is the shape every epic in this repository carried before
// klq: the definition of done as one semicolon-separated paragraph, with no
// item structure at all — the shape the refusal exists for.
const unmarkedProse = `An epic run that discovers a defect gating its own definition of done absorbs it as a tick, fixes it and closes, with no person triaging; a finding the done is reachable without becomes a backlog tick with an owner instead. Where an acceptance item is runnable, running it is the authoritative verdict and no classifier overrides it.`

// TestParseExtractsMarkedItemsInOrder: the fact sheet parses into its items,
// in document order, each with its id and the fact the mark annotates. The
// goal paragraph before the first mark is a goal statement, not a fact, and
// is not an item.
func TestParseExtractsMarkedItemsInOrder(t *testing.T) {
	t.Parallel()

	items, err := Parse(factSheet)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Item{
		{ID: "A1", Text: "The sqlite storage layer is implemented in Rust; no C sources remain in storage/."},
		{ID: "A2", Text: "The full existing test suite passes unchanged against the Rust implementation."},
		{ID: "A3", Text: "Benchmark suite `bench/storage` shows a median latency improvement of 30% or more over the C baseline in bench/baseline.json."},
		{ID: "A4", Text: "(human judgment) Release notes read well and position the change accurately."},
	}
	if len(items) != len(want) {
		t.Fatalf("Parse returned %d items, want %d: %+v", len(items), len(want), items)
	}
	for i, got := range items {
		if got != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestParseJoinsAContinuationLineToItsItem: prose that follows a mark, up to
// the next mark, is the fact the mark annotates. A fact wrapped across lines
// is not silently truncated — losing half a clause would be the same false
// negative this package exists to prevent, one level down.
func TestParseJoinsAContinuationLineToItsItem(t *testing.T) {
	t.Parallel()

	const criteria = "[A1] The storage layer is implemented in Rust;\nno C sources remain in storage/.\n[A2] The suite passes."
	items, err := Parse(criteria)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Parse returned %d items, want 2: %+v", len(items), items)
	}
	want := "The storage layer is implemented in Rust;\nno C sources remain in storage/."
	if items[0].Text != want {
		t.Errorf("A1 text = %q, want %q", items[0].Text, want)
	}
	if items[1].ID != "A2" || items[1].Text != "The suite passes." {
		t.Errorf("A2 = %+v, want {A2 The suite passes.}", items[1])
	}
}

// TestParseIgnoresMidLineReferencesAndNonMarks: a bracketed [A<n>] inside a
// sentence is a REFERENCE to an item, not a definition of one — only a
// line-leading mark defines. Lowercase [a1] and words like [Alpha] are prose.
func TestParseIgnoresMidLineReferencesAndNonMarks(t *testing.T) {
	t.Parallel()

	const criteria = "Proven by a run whose trace shows [A3] of the front epic after a cold restart.\n[a1] lowercase is prose.\n[Alpha] a bracketed word is prose."
	items, err := Parse(criteria)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("Parse returned %d items, want none: %+v", len(items), items)
	}
}

// TestParseOfUnmarkedProseYieldsNoItems: prose with no marks is zero items and
// NOT an error — the refusal, not the parser, is what answers for it. This is
// the state every epic here was in before klq.
func TestParseOfUnmarkedProseYieldsNoItems(t *testing.T) {
	t.Parallel()

	items, err := Parse(unmarkedProse)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("Parse returned %d items, want none: %+v", len(items), items)
	}
}

// TestParseRefusesMarksOutOfShape: a line-leading bracket that looks like an
// item mark but is not a stable id is a REFUSAL, not a silent skip — an epic
// would otherwise claim structure it does not have, and the unmarked-item
// refusal would never fire for it. Item zero, ids beyond 999, leading zeros
// and junk inside the brackets are all refused naming the mark.
func TestParseRefusesMarksOutOfShape(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		"[A0] item zero is not an item id",
		"[A1000] beyond the schema's range of 999",
		"[A01] a leading zero is not a stable id",
		"[A1 x] junk inside the mark",
	} {
		if _, err := Parse(line); err == nil {
			t.Errorf("Parse(%q) = nil error, want a refusal naming the mark", line)
		} else if !strings.Contains(err.Error(), "stable acceptance item id") {
			t.Errorf("Parse(%q) error = %q, want it to name the stable-id requirement", line, err)
		}
	}
}

// TestParseRefusesADuplicateMark: the same id marked twice is one fact with
// two texts, which is no fact at all. Within one container ids are unique;
// across containers they are unique too, because [evidence.acceptance] is a
// single namespace — but Parse sees one container at a time, so the
// cross-container rule is the author's, enforced where the namespaces meet.
func TestParseRefusesADuplicateMark(t *testing.T) {
	t.Parallel()

	const criteria = "[A2] first reading.\n[A2] second reading."
	_, err := Parse(criteria)
	if err == nil {
		t.Fatal("Parse = nil error, want a refusal for the duplicate id")
	}
	if !strings.Contains(err.Error(), "A2") || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Parse error = %q, want it to name A2 and say it is marked twice", err)
	}
}

// TestParsedIdsMatchTheBoundIdPattern: every id Parse accepts is an id
// [evidence.acceptance] can bind — the pattern is one definition, shared with
// the config reader, so the two ends cannot drift apart.
func TestParsedIdsMatchTheBoundIdPattern(t *testing.T) {
	t.Parallel()

	items, err := Parse(factSheet)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("the fact sheet parsed to no items")
	}
	for _, item := range items {
		if !runconfig.AcceptanceItemPattern.MatchString(item.ID) {
			t.Errorf("id %q does not match the binding pattern %s", item.ID, runconfig.AcceptanceItemPattern)
		}
	}
}

// TestResolveBindsItemsToTheirCommands: an item whose id is mapped in
// [evidence.acceptance] is RUNNABLE, carrying the id of the one command that
// proves it.
func TestResolveBindsItemsToTheirCommands(t *testing.T) {
	t.Parallel()

	items, err := Parse(factSheet)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved := Resolve(items, map[string]string{"A1": "go", "A2": "ts", "A4": "herd-helper-quick"})
	if len(resolved) != 4 {
		t.Fatalf("Resolve returned %d items, want 4: %+v", len(resolved), resolved)
	}
	for _, want := range []struct {
		id      string
		state   State
		command string
	}{
		{"A1", Runnable, "go"},
		{"A2", Runnable, "ts"},
		{"A3", Unverified, ""},
		{"A4", Runnable, "herd-helper-quick"},
	} {
		var got Resolved
		for _, r := range resolved {
			if r.ID == want.id {
				got = r
				break
			}
		}
		if got.ID != want.id {
			t.Fatalf("no item %s in %+v", want.id, resolved)
		}
		if got.State != want.state || got.Command != want.command {
			t.Errorf("%s = state %q command %q, want state %q command %q", want.id, got.State, got.Command, want.state, want.command)
		}
	}
}

// TestResolveReportsUnboundItemsAsUnverified: an item with no command bound is
// UNVERIFIED — visibly so. Collapsing it into "false" would silently make
// every unrunnable item non-gating, which is the false negative this epic
// exists to prevent: UNVERIFIED is the state that later decides classifier
// versus oracle, so it must survive the trip as itself.
func TestResolveReportsUnboundItemsAsUnverified(t *testing.T) {
	t.Parallel()

	items, err := Parse(factSheet)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved := Resolve(items, map[string]string{"A1": "go"})
	if len(resolved) != 4 {
		t.Fatalf("Resolve returned %d items, want 4: %+v", len(resolved), resolved)
	}
	third := resolved[2]
	if third.ID != "A3" {
		t.Fatalf("item 2 is %s, want A3", third.ID)
	}
	if third.State != Unverified {
		t.Errorf("A3 state = %q, want %q", third.State, Unverified)
	}
	if third.State == "" || third.State == "false" {
		t.Errorf("A3 state = %q, which is not a state at all", third.State)
	}
	if third.Command != "" {
		t.Errorf("A3 command = %q, want empty", third.Command)
	}
}

// TestResolveIgnoresBindingsForOtherContainers: [evidence.acceptance] is one
// namespace across every container a repository carries, so a map bound for
// another container's items must neither materialise items here nor be
// confused with a miss.
func TestResolveIgnoresBindingsForOtherContainers(t *testing.T) {
	t.Parallel()

	items, err := Parse("[A1] one fact.\n[A2] another fact.")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved := Resolve(items, map[string]string{"A99": "go", "A1": "ts"})
	if len(resolved) != 2 {
		t.Fatalf("Resolve returned %d items, want 2: %+v", len(resolved), resolved)
	}
	for _, r := range resolved {
		if r.ID == "A99" {
			t.Errorf("Resolve materialised A99 from the binding table: %+v", r)
		}
	}
	if resolved[0].State != Runnable || resolved[0].Command != "ts" {
		t.Errorf("A1 = %+v, want runnable on ts", resolved[0])
	}
	if resolved[1].State != Unverified {
		t.Errorf("A2 state = %q, want %q", resolved[1].State, Unverified)
	}
}

// TestResolveWithNoEvidenceTableIsAllUnverified: a repository that declares
// no [evidence.acceptance] at all still gets its items enumerated — every one
// unverified, none of them false.
func TestResolveWithNoEvidenceTableIsAllUnverified(t *testing.T) {
	t.Parallel()

	items, err := Parse(factSheet)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved := Resolve(items, nil)
	if len(resolved) != 4 {
		t.Fatalf("Resolve returned %d items, want 4: %+v", len(resolved), resolved)
	}
	for _, r := range resolved {
		if r.State != Unverified || r.Command != "" {
			t.Errorf("%s = %+v, want unverified with no command", r.ID, r)
		}
	}
}

// TestDecideRefusesAnUnmarkedAcceptance: an epic whose acceptance carries no
// items cannot decide absorption at all — it must refuse and NAME that as the
// reason, rather than guessing. The refusal is the refusal this tick was
// required to ship next to the parser: without it, shipping the absorption
// machinery would let an unmarked epic silently absorb against prose.
func TestDecideRefusesAnUnmarkedAcceptance(t *testing.T) {
	t.Parallel()

	done, refusal, err := Decide(unmarkedProse, map[string]string{"A1": "go"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if refusal == nil {
		t.Fatal("Decide returned no refusal for an unmarked acceptance, want the refusal to absorb")
	}
	if len(done.Items) != 0 {
		t.Errorf("Decide returned %d items alongside the refusal, want none: %+v", len(done.Items), done.Items)
	}
	for _, want := range []string{"no [A<n>]-marked items", "refusing to absorb"} {
		if !strings.Contains(refusal.Reason, want) {
			t.Errorf("refusal reason = %q, want it to name %q", refusal.Reason, want)
		}
	}
	if refusal.String() != refusal.Reason {
		t.Errorf("Refusal.String() = %q, want the reason %q", refusal.String(), refusal.Reason)
	}
}

// TestDecideResolvesAMarkedAcceptance: a marked acceptance decides — items
// resolved against the evidence table, runnable where a command is bound,
// unverified where none is, and no refusal.
func TestDecideResolvesAMarkedAcceptance(t *testing.T) {
	t.Parallel()

	done, refusal, err := Decide(factSheet, map[string]string{"A1": "go", "A2": "go", "A4": "herd-helper-quick"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if refusal != nil {
		t.Fatalf("Decide refused a marked acceptance: %s", refusal)
	}
	if len(done.Items) != 4 {
		t.Fatalf("Decide returned %d items, want 4: %+v", len(done.Items), done.Items)
	}
	if done.Items[0].State != Runnable || done.Items[0].Command != "go" {
		t.Errorf("A1 = %+v, want runnable on go", done.Items[0])
	}
	if done.Items[2].State != Unverified || done.Items[2].Command != "" {
		t.Errorf("A3 = %+v, want unverified with no command", done.Items[2])
	}
}

// TestDecidePropagatesParseErrors: a malformed mark is an error, not a
// refusal — the refusal speaks for marked-less acceptance; a bad mark is a
// defect in the container's own text that a person must fix.
func TestDecidePropagatesParseErrors(t *testing.T) {
	t.Parallel()

	done, refusal, err := Decide("[A0] item zero", nil)
	if err == nil {
		t.Fatal("Decide = nil error for a malformed mark, want the parse error")
	}
	if refusal != nil {
		t.Errorf("Decide also refused (%s); a malformed mark is an error, not a refusal", refusal)
	}
	if len(done.Items) != 0 {
		t.Errorf("Decide returned %d items alongside an error, want none", len(done.Items))
	}
}

// TestResolveAgainstTheRealEvidenceTable is the two ends actually meeting:
// marks parsed out of an acceptance, and the [evidence.acceptance] table as
// the config reader loads and validates it — the same object a run hands
// Decide — joined into runnable and unverified items.
func TestResolveAgainstTheRealEvidenceTable(t *testing.T) {
	t.Parallel()

	const repoConfig = `
[roles.implement]
kind = "claude"

[testing.commands]
go = { command = "go test -short ./...", description = "Go suite, short mode" }

[evidence.acceptance]
A1 = "go"
`
	cfg, err := runconfig.Parse([]byte(repoConfig))
	if err != nil {
		t.Fatalf("runconfig.Parse: %v", err)
	}
	if cfg.Evidence == nil || cfg.Evidence.Acceptance["A1"] != "go" {
		t.Fatalf("the fixture config did not carry [evidence.acceptance]: %+v", cfg.Evidence)
	}

	const criteria = "[A1] The full existing test suite passes unchanged.\n[A2] Release notes read well and position the change accurately."
	done, refusal, err := Decide(criteria, cfg.Evidence.Acceptance)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if refusal != nil {
		t.Fatalf("Decide refused a marked acceptance: %s", refusal)
	}
	if len(done.Items) != 2 {
		t.Fatalf("Decide returned %d items, want 2: %+v", len(done.Items), done.Items)
	}
	if done.Items[0].State != Runnable || done.Items[0].Command != "go" {
		t.Errorf("A1 = %+v, want runnable on the go command", done.Items[0])
	}
	if done.Items[1].State != Unverified || done.Items[1].Command != "" {
		t.Errorf("A2 = %+v, want unverified with no command", done.Items[1])
	}
}
