package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `[roles.*]` reader. It is a SECOND narrow reader of `.tick/runners.toml`,
// not a widening of the reconciler's gate reader: the gate reader parses
// `[testing.commands]` because that is the only thing the reconciler is allowed
// to RUN, and a reader that also understood roles would be one edit away from
// running something a role declared.

const rolesDocument = `# worker routing
version = 2

[orchestration]
substrate = "auto"
max_parallel = 4

[roles.implement]
kind = "claude"
model = "sonnet"
effort = "high"

[roles.implement.tiers.economy]
model = "haiku"
effort = "low"

[roles.review]
kind = "codex"
model = "gpt-5.6-luna"

[testing.commands]
go = { command = "go test ./...", description = "Go" }
`

func TestTheRolesReaderReadsKindModelAndTiers(t *testing.T) {
	roles, err := ParseRoles(rolesDocument)
	if err != nil {
		t.Fatalf("the document was refused: %v", err)
	}
	implement, ok := roles["implement"]
	if !ok {
		t.Fatalf("no [roles.implement] in %v", roles)
	}
	if implement.Kind != "claude" || implement.Model != "sonnet" {
		t.Errorf("[roles.implement] read as %+v", implement)
	}
	economy, ok := implement.Tiers["economy"]
	if !ok {
		t.Fatalf("no [roles.implement.tiers.economy] in %+v", implement)
	}
	// A tier states only what it overrides: economy names a model and no kind,
	// and reading an empty kind as "no runner" is what makes the overlay an
	// overlay rather than a replacement.
	if economy.Model != "haiku" || economy.Kind != "" {
		t.Errorf("the economy tier read as %+v", economy)
	}
	if review := roles["review"]; review.Kind != "codex" || review.Model != "gpt-5.6-luna" {
		t.Errorf("[roles.review] read as %+v", review)
	}
}

// Every other table in the file configures somebody else's process. This reader
// reads none of them — including the one the reconciler DOES run.
func TestTheRolesReaderReadsNoOtherTable(t *testing.T) {
	roles, err := ParseRoles(rolesDocument)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 {
		t.Fatalf("the reader returned %d roles from a document with two: %v", len(roles), roles)
	}
	for _, name := range []string{"testing", "orchestration", "commands", "go"} {
		if _, found := roles[name]; found {
			t.Errorf("the reader read %q, which is not a role", name)
		}
	}
}

// A key/value inside a roles table that this reader cannot read is REFUSED,
// with the line named. Routing that silently falls back to a default is routing
// an operator cannot see they lost.
func TestARolesTableThisReaderCannotReadIsRefused(t *testing.T) {
	for _, document := range []string{
		"[roles.implement]\nkind\n",
		"[roles.implement]\nkind = claude\n",
		"[roles.implement\nkind = \"claude\"\n",
	} {
		if _, err := ParseRoles(document); err == nil {
			t.Errorf("%q was accepted", document)
		} else if !strings.Contains(err.Error(), "runners.toml") {
			t.Errorf("%q was refused with %q, which does not say where", document, err)
		}
	}
}

// A key ticks' own schema defines but this reader does not route on yet
// (`effort`, `args`, and — at the role level — `harness`) is IGNORED rather
// than refused: the pinned shape allows it, and ticfac simply has nowhere to
// put it yet.
func TestAKeyThisReaderDoesNotOwnButTheShapeAllowsIsIgnored(t *testing.T) {
	roles, err := ParseRoles("[roles.implement]\nkind = \"claude\"\neffort = \"high\"\nargs = [\"--foo\"]\nharness = \"claude\"\n")
	if err != nil {
		t.Fatalf("a role with keys the pinned shape allows was refused: %v", err)
	}
	if roles["implement"].Kind != "claude" {
		t.Errorf("[roles.implement] read as %+v", roles["implement"])
	}
}

// A key outside the pinned shape entirely is a malformed entry, not a future
// schema addition to route around silently: it is REFUSED, naming the file,
// the table and the line.
func TestAKeyOutsideThePinnedShapeIsRefused(t *testing.T) {
	_, err := ParseRoles("[roles.implement]\nkind = \"claude\"\nbudget_usd = 5\n")
	if err == nil {
		t.Fatal("a role with a key outside the pinned shape was accepted")
	}
	if !strings.Contains(err.Error(), "runners.toml") || !strings.Contains(err.Error(), "budget_usd") {
		t.Errorf("refused with %q, which does not name the file and the offending key", err)
	}
}

// `harness` is documentary and valid only at the role level per ticks'
// schema — never on a tier overlay, which has no field for it.
func TestHarnessOnATierOverlayIsRefused(t *testing.T) {
	_, err := ParseRoles("[roles.implement]\nkind = \"claude\"\n\n[roles.implement.tiers.economy]\nmodel = \"haiku\"\nharness = \"claude\"\n")
	if err == nil {
		t.Fatal("harness on a tier overlay was accepted")
	}
	if !strings.Contains(err.Error(), "runners.toml") || !strings.Contains(err.Error(), "harness") {
		t.Errorf("refused with %q, which does not name the file and the offending key", err)
	}
}

// A file that is not there is not an error the reader invents a default for:
// the caller decides what an absent routing means, and here it means the
// profiles ship as written.
func TestAMissingRunnersConfigIsNoRoutingRatherThanAnError(t *testing.T) {
	roles, err := ReadRoles(filepath.Join(t.TempDir(), "runners.toml"))
	if err != nil {
		t.Fatalf("a missing file was an error: %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("a missing file produced %v", roles)
	}
}

func TestReadRolesReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runners.toml")
	if err := os.WriteFile(path, []byte(rolesDocument), 0o644); err != nil {
		t.Fatal(err)
	}
	roles, err := ReadRoles(path)
	if err != nil {
		t.Fatal(err)
	}
	if roles["implement"].Kind != "claude" {
		t.Errorf("%s read as %v", path, roles)
	}
}
