package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The tests in this file pin the SPLIT, not the format: that this reader owns
// the execution tables and tolerates the tracker tables, and that a file
// written by the single migrator — the one writer, on ticks' side of the split
// — still loads here with both halves intact. The decision they enforce is
// logged on epic av8 (2026-09-10).

const trackerTablesDocument = `# a repository whose runs are swept and signalled
version = 2

[orchestration]
substrate = "auto"

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
go = { command = "go test -short -count=1 ./...", description = "Go" }

[signals.sources.github]
secret = "SIGNAL_SECRET_GITHUB"
header = "X-Hub-Signature-256"
algorithm = "hmac-sha256"
encoding = "hex"
external_ref = "data.issue.id"
title = "data.issue.title"
type = "task"
priority = 2
labels = ["signal"]

[sweeps.nightly]
cron = "0 4 * * 1-5"
filter = "type:bug priority<=2 unblocked"
max_ticks = 3
budget_usd = 25
tier = "standard"
gate_on_complete = "telegram"
`

// TestTrackerTablesAreForeignNotRefused: a file declaring ticks' half of the
// split loads in this reader. The tracker tables are neither validated nor
// refused — a legal file on the other side of the split is not a typo'd key on
// this side. This is the tolerance that lets ticks add or change tracker
// policy without this reader failing a repository's good file.
func TestTrackerTablesAreForeignNotRefused(t *testing.T) {
	cfg, err := Parse([]byte(trackerTablesDocument))
	if err != nil {
		t.Fatalf("a file carrying [signals] and [sweeps] was refused: %v", err)
	}
	if got := cfg.Roles["implement"].Model; got != "sonnet" {
		t.Errorf("roles.implement.model = %q, want sonnet", got)
	}
	if got := cfg.Substrate(); got != SubstrateAuto {
		t.Errorf("Substrate() = %q, want auto", got)
	}
	if cfg.Testing == nil || cfg.Testing.Commands["go"].Command != "go test -short -count=1 ./..." {
		t.Errorf("[testing.commands] was not read: %+v", cfg.Testing)
	}
}

// TestATypoInsideATrackerTableIsNotThisReadersOpinion: the tolerance is keyed
// on the table, not the key. A typo inside [signals] is a real defect — but it
// is TICKS' defect to report at author time, in the tool that validates that
// half; this reader refusing it would break the split it exists to enforce.
func TestATypoInsideATrackerTableIsNotThisReadersOpinion(t *testing.T) {
	src := strings.Replace(trackerTablesDocument,
		`external_ref = "data.issue.id"`,
		`external_refz = "data.issue.id"`, 1)
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("a typo'd key inside a foreign table was refused here: %v", err)
	}
}

// TestAnUnknownTableIsStillRefused: the tolerance ends at the enumeration. A
// table neither reader owns is a typo'd table, and this reader says so —
// refusing it is what keeps the tolerance from becoming a way for an unknown
// execution key to hide.
func TestAnUnknownTableIsStillRefused(t *testing.T) {
	for _, extra := range []string{
		"[frobnicate]\nsetting = 11\n",
		"[orchestrationz]\nsubstrate = \"auto\"\n",
		"[signalsz.sources.github]\nsecret = \"x\"\n",
	} {
		if _, err := Parse([]byte("[roles.implement]\nkind = \"claude\"\n\n" + extra)); err == nil {
			t.Errorf("%q was accepted", extra)
		} else if !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("%q was refused without naming the unknown key: %v", extra, err)
		}
	}
}

// TestAKeyOutsideAnyTableIsRefused pins the boundary from the scalar side: the
// top-level key set is closed too, exactly as the schema's
// additionalProperties: false says.
func TestAKeyOutsideAnyTableIsRefused(t *testing.T) {
	if _, err := Parse([]byte("signals = true\n\n[roles.implement]\nkind = \"claude\"\n")); err == nil {
		t.Fatal("a scalar named like a foreign table was accepted")
	}
	if _, err := Parse([]byte("[roles.implement]\nkind = \"claude\"\n\n[signals]\nsources = \"not a table\"\n")); err != nil {
		t.Fatalf("[signals] with a malformed value was refused as if it were this reader's table: %v", err)
	}
}

// trackerShape is the other reader's half, decoded here only to prove those
// bytes are intact in a file the migrator wrote. It is a MIRROR for one test,
// not a second reader: nothing in this repository validates signals or sweeps
// against the schema, and this struct deliberately has no opinion — it records
// what the tables say so the fixture can be shown unchanged.
type trackerShape struct {
	Signals struct {
		Sources map[string]struct {
			Secret      string `toml:"secret"`
			Header      string `toml:"header"`
			Algorithm   string `toml:"algorithm"`
			Encoding    string `toml:"encoding"`
			ExternalRef string `toml:"external_ref"`
			Title       string `toml:"title"`
			Type        string `toml:"type"`
			Priority    *int   `toml:"priority"`
		} `toml:"sources"`
	} `toml:"signals"`
	Sweeps map[string]struct {
		Cron           string   `toml:"cron"`
		Filter         string   `toml:"filter"`
		MaxTicks       *int     `toml:"max_ticks"`
		BudgetUSD      *float64 `toml:"budget_usd"`
		Tier           string   `toml:"tier"`
		GateOnComplete string   `toml:"gate_on_complete"`
	} `toml:"sweeps"`
}

// TestAFileWrittenByTheSingleMigratorLoadsInBothHalves is the split's central
// obligation, as bytes: testdata/old-migrator.runners.toml was produced by the
// real `tk config migrate --write` from a repository that had routing, the
// legacy command surface in config.md, and the tracker tables already in the
// file. The migrator — the ONE writer, on ticks' side — merged the command
// surface in, preserved the tracker tables, and bumped the version to 2.
//
// Both readers must load what it writes:
//
//   - this package loads the execution half and tolerates the rest — asserted
//     here, against the committed bytes;
//   - ticks' reader loads its own output by its own suite (its TestMigrate*
//     tests end with exactly that assertion), and the tracker half is decoded
//     here only to prove the migrator did not corrupt it.
//
// If a future format change makes this test fail, the file was written by a
// NEWER migrator than this reader understands: regenerate the fixture at the
// new pin and read the version gate's upgrade line, never loosen the reader.
func TestAFileWrittenByTheSingleMigratorLoadsInBothHalves(t *testing.T) {
	path := filepath.Join("testdata", "old-migrator.runners.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	// The execution half, read the way the orchestrator reads it.
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("a file written by the single migrator was refused: %v", err)
	}
	if got := cfg.DeclaredVersion(); got != Version {
		t.Errorf("declared version = %d, want %d", got, Version)
	}
	if got := cfg.Roles["implement"].Model; got != "sonnet" {
		t.Errorf("roles.implement.model = %q, want sonnet", got)
	}
	if got := cfg.Roles["review"].Model; got != "opus" {
		t.Errorf("roles.review.model = %q, want opus", got)
	}
	if cfg.Testing == nil || cfg.Testing.Commands["go"].Command != "go test -short -count=1 ./..." {
		t.Errorf("[testing.commands] did not survive the migration: %+v", cfg.Testing)
	}
	if cfg.Evidence == nil || cfg.Evidence.Acceptance["A2"] != "go" {
		t.Errorf("[evidence.acceptance] did not survive the migration: %+v", cfg.Evidence)
	}
	if cfg.Environment == nil || cfg.Environment.Commands["git-identity"].Command != "git config user.email" {
		t.Errorf("[environment.commands] did not survive the migration: %+v", cfg.Environment)
	}

	// The tracker half, decoded only to prove the migrator left it intact:
	// these are ticks' tables and this package has no validator for them, but
	// a file whose other half was corrupted by the migration would be a file
	// the split broke, whichever side noticed first.
	var tracker trackerShape
	if _, err := toml.Decode(string(raw), &tracker); err != nil {
		t.Fatalf("the fixture's tracker half does not decode: %v", err)
	}
	src, ok := tracker.Signals.Sources["example"]
	if !ok {
		t.Fatalf("the migration dropped [signals.sources.example]: %+v", tracker.Signals)
	}
	if src.Secret != "SIGNAL_SECRET_EXAMPLE" || src.Header != "X-Signature" || *src.Priority != 2 {
		t.Errorf("[signals.sources.example] was corrupted: %+v", src)
	}
	sweep, ok := tracker.Sweeps["nightly"]
	if !ok {
		t.Fatalf("the migration dropped [sweeps.nightly]: %+v", tracker.Sweeps)
	}
	if sweep.Cron != "0 4 * * 1-5" || sweep.Tier != "standard" || *sweep.BudgetUSD != 25 {
		t.Errorf("[sweeps.nightly] was corrupted: %+v", sweep)
	}
}

// TestLoadRepoLoadsTheMigratorsFileThroughTheRepoRoot: the fixture through the
// entry point the orchestrator actually calls, missing-file semantics and all.
func TestLoadRepoLoadsTheMigratorsFileThroughTheRepoRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "old-migrator.runners.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(FileName)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRepo(dir)
	if err != nil || cfg == nil {
		t.Fatalf("LoadRepo on the migrator's file: %v, %v", cfg, err)
	}
}
