package parity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/run-event-feed.json — EXECUTABLE.
//
// The run event feed: one append-only JSONL stream per run, at
// `.ticfac/logs/<run-id>/events.jsonl`, that a non-participant subscribes to
// and follows to learn WHEN TO LOOK. Written by the reconciler
// (internal/reconcile, through internal/runfeed); read by anyone.
//
// Two things are executed here rather than described:
//
//   - the line schema (`ticfac.run_event.v1`) admits its golden documents and
//     refuses its negative ones, with the pinned refusal — the same documents
//     the WRITER's own tests replay, so the fixture is a second reader of the
//     bytes and not a decoration beside them;
//   - the CROSS-FILE CLAIM, the thing VerifySchemaIDs cannot see: the feed's
//     path is exhaust under the `.ticfac/logs/` entry ticfac-run-state.json
//     already carries, so this contract adds a path without touching that
//     contract's layout, and the claim is checked rather than trusted.
//
// The rule the whole fixture turns on — a line means *worth looking now*,
// never *the work is finished*; completion is decided by durable evidence
// alone; a lost, late or untruthful line changes no verdict — is asserted as
// words the readers refuse to let a dashboard soften, because a rule that
// lives only in prose is the one that gets relearned at 15 minutes of blind
// sleep a time.

const runEventFeedFile = "run-event-feed.json"

type runEventFeed struct {
	SchemaVersion int    `json:"schema_version"`
	Contract      string `json:"contract"`
	Layout        struct {
		Path        string `json:"path"`
		Committed   bool   `json:"committed"`
		Rebuildable bool   `json:"rebuildable"`
		Cardinality string `json:"cardinality"`
	} `json:"layout"`
	Identity struct {
		EveryLineCarries []string `json:"every_line_carries"`
	} `json:"identity"`
	AppendOnly struct {
		LinesAreOnlyEverAppended       bool   `json:"lines_are_only_ever_appended"`
		ALineIsNeverRewrittenOrRemoved bool   `json:"a_line_is_never_rewritten_or_removed"`
		OneJSONObjectPerLine           bool   `json:"one_json_object_per_line"`
		NewlineTerminated              bool   `json:"newline_terminated"`
		AReaderResumesFrom             string `json:"a_reader_resumes_from"`
	} `json:"append_only"`
	Subscribe struct {
		ALineMeans                  string `json:"a_line_means"`
		CompletionIsDecidedBy       string `json:"completion_is_decided_by"`
		ALostOrLateOrUntruthfulLine string `json:"a_lost_or_late_or_untruthful_line"`
	} `json:"subscribe"`
	Boundary struct {
		OnlyWriter   string `json:"only_writer"`
		WorkersWrite bool   `json:"workers_write"`
		IsAuthority  bool   `json:"is_authority"`
	} `json:"boundary"`
	Records map[string]struct {
		SchemaID string          `json:"schema_id"`
		Schema   json.RawMessage `json:"schema"`
	} `json:"records"`
	Golden  map[string]json.RawMessage `json:"golden"`
	Invalid []struct {
		Record              string          `json:"record"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
}

func loadRunEventFeed(t *testing.T) (runEventFeed, map[string]*schema.Schema) {
	t.Helper()
	var c runEventFeed
	readContract(t, runEventFeedFile, &c)

	schemas := map[string]*schema.Schema{}
	for name, record := range c.Records {
		schemas[name] = parseSchema(t, "records."+name+".schema", record.Schema)
	}
	return c, schemas
}

func TestRunEventFeedIdentifiesItself(t *testing.T) {
	c, _ := loadRunEventFeed(t)
	if c.SchemaVersion != 1 || c.Contract != "ticfac.run_event_feed" {
		t.Errorf("the contract does not identify itself: %q v%d", c.Contract, c.SchemaVersion)
	}
	if c.Layout.Path != ".ticfac/logs/<run-id>/events.jsonl" {
		t.Errorf("the feed lives at %s", c.Layout.Path)
	}
	if c.Layout.Committed {
		t.Error("the feed is committed; it is exhaust, and durable means pushed — a feed at the run's write cadence is not a record")
	}
	if c.Layout.Rebuildable {
		t.Error("the feed is rebuildable; the journal events it carries are recorded nowhere else")
	}
	if c.Layout.Cardinality != "exactly one per run" {
		t.Errorf("cardinality %q — two feeds interleaved is neither append-only nor a feed", c.Layout.Cardinality)
	}
	if c.Boundary.OnlyWriter != "the reconciler" || c.Boundary.WorkersWrite {
		t.Errorf("the boundary is not closed: only_writer=%q workers_write=%v", c.Boundary.OnlyWriter, c.Boundary.WorkersWrite)
	}
	if c.Boundary.IsAuthority {
		t.Error("the feed is authority; the durable truth stays on the integration branch")
	}
}

func TestRunEventFeedIsAppendOnly(t *testing.T) {
	c, _ := loadRunEventFeed(t)
	a := c.AppendOnly
	if !a.LinesAreOnlyEverAppended || !a.ALineIsNeverRewrittenOrRemoved ||
		!a.OneJSONObjectPerLine || !a.NewlineTerminated {
		t.Errorf("append-only is not pinned as rules: %+v", a)
	}
	if !strings.Contains(a.AReaderResumesFrom, "offset") {
		t.Errorf("a reader resumes from %q, not from an offset", a.AReaderResumesFrom)
	}
}

func TestRunEventFeedCarriesIdentityOnEveryLine(t *testing.T) {
	c, _ := loadRunEventFeed(t)
	if len(c.Identity.EveryLineCarries) != 3 {
		t.Errorf("identity on every line names %d fields, want run, tick and attempt", len(c.Identity.EveryLineCarries))
	}
	for _, id := range c.Identity.EveryLineCarries {
		switch id {
		case "run_id", "tick_id", "attempt":
		default:
			t.Errorf("identity on every line names %q", id)
		}
	}
}

func TestRunEventFeedStatesTheHintRuleInWordsNobodyCanSoften(t *testing.T) {
	c, _ := loadRunEventFeed(t)
	// The rule tk herd wait's own documentation states, and the one this
	// whole contract exists to keep: the signal wakes, the evidence decides.
	if !strings.Contains(c.Subscribe.ALineMeans, "worth looking now") ||
		!strings.Contains(c.Subscribe.ALineMeans, "never") {
		t.Errorf("a_line_means does not state the rule: %q", c.Subscribe.ALineMeans)
	}
	if !strings.Contains(c.Subscribe.CompletionIsDecidedBy, "durable evidence") {
		t.Errorf("completion_is_decided_by does not name durable evidence: %q", c.Subscribe.CompletionIsDecidedBy)
	}
	if !strings.Contains(c.Subscribe.ALostOrLateOrUntruthfulLine, "changes no verdict") {
		t.Errorf("the lost-signal rule is softened: %q", c.Subscribe.ALostOrLateOrUntruthfulLine)
	}
}

func TestRunEventFeedSchemasAdmitTheirGoldenDocuments(t *testing.T) {
	c, schemas := loadRunEventFeed(t)
	if len(schemas) != 1 {
		t.Errorf("the contract declares %d record schemas, want the one line schema", len(schemas))
	}
	for name := range schemas {
		if _, ok := c.Golden[name]; !ok {
			t.Errorf("record %s has no golden example", name)
		}
	}
	// Every golden is a line of the one schema, under either key — dispatch
	// scoped and run-level — and a golden nothing validates is a document
	// pretending to be a rule.
	if len(c.Golden) < 2 {
		t.Errorf("the contract carries %d golden lines; the null-identity run-level case and the dispatch case are both rules", len(c.Golden))
	}
	for name, document := range c.Golden {
		var errs []string
		for _, s := range schemas {
			errs = schema.Validate(s, nil, decodeDocument(t, document))
			if len(errs) == 0 {
				break
			}
		}
		if len(errs) > 0 {
			t.Errorf("golden %s is refused by the line schema:\n  %s", name, strings.Join(errs, "\n  "))
		}
	}
}

func TestRunEventFeedRefusesEveryNegativeDocument(t *testing.T) {
	c, schemas := loadRunEventFeed(t)
	if len(c.Invalid) == 0 {
		t.Fatal("no negative example")
	}
	for _, bad := range c.Invalid {
		s, ok := schemas[bad.Record]
		if !ok {
			t.Errorf("%s: negative case for unknown record %q", bad.Why, bad.Record)
			continue
		}
		errs := schema.Validate(s, nil, decodeDocument(t, bad.Document))
		if len(errs) == 0 {
			t.Errorf("a document the contract calls invalid was accepted: %s", bad.Why)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), bad.ExpectErrorContains) {
			t.Errorf("%s\n  refused with %s\n  contract expects %s",
				bad.Why, strings.Join(errs, "\n  "), bad.ExpectErrorContains)
		}
	}
}

func TestRunEventFeedExhaustClaimMatchesTheRunStateContract(t *testing.T) {
	c, _ := loadRunEventFeed(t)

	// The cross-file claim: the feed's path is exhaust under the
	// `.ticfac/logs/` entry ticfac-run-state.json already carries, which is
	// why this contract adds a path without touching that contract's layout.
	// VerifySchemaIDs cannot see this — a path is not a schema id — so the
	// readers hold the seam.
	var runState struct {
		Layout struct {
			Entries []struct {
				Path      string `json:"path"`
				Committed bool   `json:"committed"`
			} `json:"entries"`
		} `json:"layout"`
		Gitignore struct {
			Fragment []string `json:"fragment"`
		} `json:"gitignore"`
	}
	readContract(t, runStateFile, &runState)

	logsEntry := false
	for _, entry := range runState.Layout.Entries {
		if entry.Path == ".ticfac/logs/" && !entry.Committed {
			logsEntry = true
		}
	}
	if !logsEntry {
		t.Fatal("ticfac-run-state.json no longer declares .ticfac/logs/ as uncommitted exhaust")
	}
	if !strings.HasPrefix(c.Layout.Path, ".ticfac/logs/") {
		t.Errorf("the feed at %s is no longer under the run-state contract's exhaust directory", c.Layout.Path)
	}
	covered := false
	for _, line := range runState.Gitignore.Fragment {
		if strings.TrimSpace(line) == ".ticfac/logs/" {
			covered = true
		}
	}
	if !covered {
		t.Error("the run-state contract's gitignore fragment no longer ignores .ticfac/logs/, so the feed would dirty the run's tree")
	}
}

// The feed's schema carries no $refs, so its documents validate against no
// defs — and a reader that resolved refs against nothing would be a reader
// asserting less than it appears to.
