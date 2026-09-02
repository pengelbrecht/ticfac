package parity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/tk-json-manifest.json — EXECUTABLE.
//
// This is contract 1: the whole tracker API ticfac is allowed to use
// (SPEC §3.1, "tk --json is the only tracker API"). It is the odd one out in
// the bundle — a PUBLISHED SURFACE rather than an input -> expected table —
// and its second implementation is whatever consumes released tk behaviour.
// ticfac is that consumer.
//
// ticks' own reader runs every command it lists against a fixture repository.
// This one cannot: there is no tk on the path here, by design — vendoring a tk
// binary into ticfac's CI would make the two repositories one build again.
// What this reader asserts instead is everything a consumer must be able to
// rely on BEFORE it calls anything:
//
//   - every published schema parses in the strict subset, and every $ref in it
//     resolves — a schema ticfac cannot express is a command ticfac cannot
//     validate the output of;
//   - the surface is closed and internally consistent: unique ids, argv that
//     matches the command, `since` within the contract;
//   - the schemas are OPEN, which is the rule that lets tk add a field
//     without breaking ticfac;
//   - the refusal a consumer pins itself with (exit 11) is published.
//
// The behavioural half — that a real tk produces documents these schemas
// admit — belongs to the tk client tick, and it validates against this file.

const tkManifestFile = "tk-json-manifest.json"

type tkManifest struct {
	Contract           int    `json:"contract"`
	SupportedContracts []int  `json:"supported_contracts"`
	MinTkVersion       string `json:"min_tk_version"`
	Comment            string `json:"$comment"`
	Request            struct {
		Flag                string `json:"flag"`
		Env                 string `json:"env"`
		Placement           string `json:"placement"`
		UnsupportedExitCode int    `json:"unsupported_exit_code"`
		UnsupportedBehavior string `json:"unsupported_behavior"`
	} `json:"request"`
	Hosts struct {
		RunsTk      string `json:"runs_tk"`
		CannotRunTk string `json:"cannot_run_tk"`
		Proof       string `json:"proof"`
	} `json:"hosts"`
	Defs     map[string]json.RawMessage `json:"$defs"`
	Commands []struct {
		ID          string            `json:"id"`
		Command     string            `json:"command"`
		Kind        string            `json:"kind"`
		Argv        []string          `json:"argv"`
		Output      string            `json:"output"`
		Since       int               `json:"since"`
		Description string            `json:"description"`
		Schema      json.RawMessage   `json:"schema"`
		ExitCodes   map[string]string `json:"exit_codes"`
	} `json:"commands"`
}

func TestTkManifestPublishesAContractTicfacCanPin(t *testing.T) {
	var m tkManifest
	readContract(t, tkManifestFile, &m)

	if m.Contract <= 0 {
		t.Fatalf("contract = %d; a consumer pins this number by exact value", m.Contract)
	}
	serves := false
	for _, n := range m.SupportedContracts {
		if n == m.Contract {
			serves = true
		}
	}
	if !serves {
		t.Errorf("supported_contracts %v does not include the contract it serves (%d)", m.SupportedContracts, m.Contract)
	}
	if m.MinTkVersion == "" {
		t.Error("no min_tk_version: a consumer cannot say which tk release serves this contract")
	}

	// The fail-closed half. ticfac declares the contract it was built against
	// and a tk that cannot serve it must refuse BEFORE running the command —
	// with its own exit code, so "install a different tk" is distinguishable
	// from a refusal or a usage error without parsing stderr.
	if m.Request.Flag == "" || m.Request.Env == "" {
		t.Error("the manifest publishes no way for a consumer to pin the contract")
	}
	switch m.Request.UnsupportedExitCode {
	case 0, 1, 2:
		t.Errorf("unsupported_exit_code = %d; it must have its own slot, not share one with success, a refusal or a usage error",
			m.Request.UnsupportedExitCode)
	}
	if m.Request.Placement == "" || m.Request.UnsupportedBehavior == "" {
		t.Error("the manifest must say where the flag goes and what happens when the contract cannot be served")
	}

	// The hosts block is part of the contract, not a design note: a host that
	// cannot run tk implements this same contract in its own language and
	// proves it with the bundle's fixtures.
	if m.Hosts.RunsTk == "" || m.Hosts.CannotRunTk == "" || m.Hosts.Proof == "" {
		t.Error("the hosts block must say who runs tk, who cannot, and what stands in for it")
	}
}

// Every published schema parses in the strict subset and every $ref resolves.
// A schema ticfac cannot express is a command whose output ticfac cannot
// validate — and, worse, one whose constraints no reader is enforcing.
func TestEveryPublishedSchemaParsesAndResolves(t *testing.T) {
	var m tkManifest
	readContract(t, tkManifestFile, &m)

	defs := parseDefs(t, m.Defs)
	if len(defs) == 0 {
		t.Fatal("the manifest declares no $defs")
	}

	used := map[string]bool{}
	for _, name := range sortedKeys(defs) {
		collectRefs(defs[name], used)
	}
	for _, c := range m.Commands {
		if c.Output != "json" {
			if len(c.Schema) != 0 {
				t.Errorf("%s: output is %q and it still publishes a schema", c.ID, c.Output)
			}
			continue
		}
		if len(c.Schema) == 0 {
			t.Errorf("%s: output is json and it publishes no schema", c.ID)
			continue
		}
		s := parseSchema(t, c.ID, c.Schema)
		collectRefs(s, used)

		// Open on purpose: within a contract version tk may ADD a field, and
		// only a removal or a type change is a break. A closed schema here
		// would make every added field a silent incompatibility.
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			t.Errorf("%s: the schema is closed; adding a field to tk's output would break every consumer",
				c.ID)
		}
	}

	for _, name := range sortedKeys(defs) {
		if !used[name] {
			t.Errorf("$defs.%s is declared and nothing references it", name)
		}
	}
	for name := range used {
		if _, ok := defs[name]; !ok {
			t.Errorf("a schema references $defs.%s, which is not declared", name)
		}
	}
}

func collectRefs(s *schema.Schema, into map[string]bool) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		into[strings.TrimPrefix(s.Ref, "#/$defs/")] = true
	}
	for _, sub := range s.Properties {
		collectRefs(sub, into)
	}
	collectRefs(s.Items, into)
	for _, alt := range s.AnyOf {
		collectRefs(alt, into)
	}
}

// The surface is closed: unique ids, argv that actually invokes the command it
// names, `--json` on every command that promises JSON, and nothing published
// as `since` a contract this manifest does not serve.
func TestTheCommandSurfaceIsInternallyConsistent(t *testing.T) {
	var m tkManifest
	readContract(t, tkManifestFile, &m)

	if len(m.Commands) == 0 {
		t.Fatal("the manifest publishes no commands")
	}

	ids := map[string]bool{}
	for _, c := range m.Commands {
		if c.ID == "" {
			t.Error("a command has no id")
			continue
		}
		if ids[c.ID] {
			t.Errorf("command id %q appears twice; ids are how a consumer addresses one entry", c.ID)
		}
		ids[c.ID] = true

		if c.Description == "" {
			t.Errorf("%s: no description", c.ID)
		}
		switch c.Kind {
		case "read", "write":
		default:
			t.Errorf("%s: kind %q is neither read nor write", c.ID, c.Kind)
		}
		if len(c.Argv) == 0 {
			t.Errorf("%s: no argv", c.ID)
			continue
		}
		if c.Argv[0] != c.Command {
			t.Errorf("%s: argv starts with %q and the command is %q", c.ID, c.Argv[0], c.Command)
		}
		if c.Output == "json" && !contains(c.Argv, "--json") {
			t.Errorf("%s: promises json output and its argv carries no --json", c.ID)
		}
		if c.Output == "exit-code" && len(c.ExitCodes) == 0 {
			t.Errorf("%s: its output IS its exit code and it publishes none", c.ID)
		}
		if c.Since <= 0 || c.Since > m.Contract {
			t.Errorf("%s: since = %d, outside the contract this manifest serves (%d)", c.ID, c.Since, m.Contract)
		}
	}
}

// The manifest records what it does NOT publish. SPEC §3.1's illustrative call
// list named commands that have no --json flag at all, and a consumer
// reimplementing from that list would have blocked on an empty stdin. The gap
// is written into the file's top-level $comment, and publishing one of those
// commands without editing the note fails here — which is what makes the note
// part of the contract rather than decoration.
func TestTheManifestRecordsWhatItDoesNotPublish(t *testing.T) {
	var m tkManifest
	readContract(t, tkManifestFile, &m)

	if !strings.Contains(m.Comment, "sandbox") || !strings.Contains(m.Comment, "ask") {
		t.Fatalf("the top-level $comment no longer records the SPEC §3.1 commands this manifest does "+
			"NOT publish; a consumer reading §3.1 alone would call them:\n%s", m.Comment)
	}
	for _, c := range m.Commands {
		switch c.Command {
		case "sandbox", "ask":
			t.Errorf("%s is published now, and the $comment still says it is not", c.Command)
		}
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
