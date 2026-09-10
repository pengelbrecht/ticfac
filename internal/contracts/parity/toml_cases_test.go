package parity

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/profile"
)

// Three `.tick/runners.toml` case tables — STRUCTURAL:
//
//	contracts/sandbox-image-cases.json
//	contracts/signal-source-cases.json
//	contracts/sweep-policy-cases.json
//
// Each is INPUT (a TOML document) -> EXPECTED (accepted with a parsed result,
// or refused). ticfac has two readers of `.tick/runners.toml`, and each reads
// one table family: internal/reconcile's reads `[testing.commands]`, the
// integrated gate, because that is the only thing the reconciler is allowed to
// RUN; internal/profile's reads `[roles.*]`, the routing a job is dispatched
// under. Neither parses `[sandbox]`, `[signals]` or `[sweeps]`, so the outcomes
// these three tables pin still cannot be executed here, and pretending
// otherwise would be the failure contracts/README.md names: a check that reads
// as if it asserted something while asserting nothing.
//
// What these readers do instead is hold the tables to the properties a case
// table has to have to be worth executing later, and — where the fixture's
// data crosses into another contract — follow it. The image cases' expected
// values are checked against runners-config-contract.json's image pattern,
// which is a real cross-file assertion: the two files disagreeing means one of
// them is wrong today, not when a TOML reader arrives.
//
// One part of them IS executable now. Every case document in all three tables
// is a WHOLE `.tick/runners.toml`, and every one of them declares
// `[roles.implement]` — because ticks' own validator requires it. ticfac has a
// reader of that table since tick q4u (internal/profile), so the documents are
// run through it below: every one of those documents is a real configuration
// file, and each must be read the same way. It is the whole of what this repository can execute against
// these tables today, and it is not nothing — a roles reader that choked on a
// multi-line string, an inline table or a `__proto__` key would fail here.

type tomlCase struct {
	Name     string   `json:"name"`
	Why      any      `json:"why"`
	TOML     string   `json:"toml"`
	Accepted bool     `json:"accepted"`
	Refused  bool     `json:"refused"`
	Image    *string  `json:"image"`
	Sources  []string `json:"sources"`
	Sweeps   []string `json:"sweeps"`

	// present records which keys the case actually carries. The three tables
	// spell an outcome differently — two use an explicit `accepted`, the image
	// table uses the presence of `image`, whose value may legitimately be null
	// — and a reader that could not tell "absent" from "null" would read the
	// 99% path as no outcome at all.
	present map[string]bool
}

func (c *tomlCase) UnmarshalJSON(data []byte) error {
	type plain tomlCase
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*c = tomlCase(value)
	c.present = make(map[string]bool, len(keys))
	for key := range keys {
		c.present[key] = true
	}
	return nil
}

type tomlCaseTable struct {
	Why   []string   `json:"why"`
	Cases []tomlCase `json:"cases"`
}

// checkTable holds one table to the shape every case table needs: named cases,
// a stated reason, a TOML input, and exactly one expected outcome. A case with
// neither outcome asserts nothing; a case with both asserts a contradiction.
//
// `outcome` is per-table because the three tables spell their outcome
// differently, and `pinsResult` says whether an accepted case pins what was
// parsed rather than only that nothing failed.
func checkTable(t *testing.T, file string, table tomlCaseTable,
	outcome func(tomlCase) (accepted, refused bool), pinsResult func(tomlCase) bool) {
	t.Helper()

	if len(table.Why) == 0 {
		t.Errorf("%s does not say why it exists", file)
	}
	if len(table.Cases) == 0 {
		t.Fatalf("%s carries no cases", file)
	}

	names := map[string]bool{}
	acceptCount, refuseCount := 0, 0
	for _, c := range table.Cases {
		if c.Name == "" {
			t.Errorf("%s: a case has no name", file)
			continue
		}
		if names[c.Name] {
			t.Errorf("%s: case %q appears twice", file, c.Name)
		}
		names[c.Name] = true
		if c.Why == nil {
			t.Errorf("%s/%s says nothing about what it proves", file, c.Name)
		}
		if strings.TrimSpace(c.TOML) == "" {
			t.Errorf("%s/%s has no input document", file, c.Name)
		}
		accepted, refused := outcome(c)
		if accepted == refused {
			t.Errorf("%s/%s declares accepted=%v and refused=%v; a case has exactly one outcome",
				file, c.Name, accepted, refused)
			continue
		}
		if accepted {
			acceptCount++
			if !pinsResult(c) {
				t.Errorf("%s/%s is accepted and pins no parsed result; then nothing is asserted but the absence of an error",
					file, c.Name)
			}
		} else {
			refuseCount++
		}
	}
	if acceptCount == 0 || refuseCount == 0 {
		t.Errorf("%s has %d accepted and %d refused cases; a table testing one direction tests half a rule",
			file, acceptCount, refuseCount)
	}
}

// The image cases, and the cross-file check that makes this reader worth
// having today: every image the table says is parsed out of a TOML document
// must be one runners-config-contract.json's pattern accepts. The two files
// pin the same rule from different angles, and nothing else in the bundle
// compares them.
func TestSandboxImageCases(t *testing.T) {
	const file = "sandbox-image-cases.json"
	var table tomlCaseTable
	readContract(t, file, &table)

	// An accepted image case pins a value — including the null that means "no
	// image was declared", which is the 99% path and the one a line-oriented
	// scanner gets wrong.
	checkTable(t, file, table,
		func(c tomlCase) (bool, bool) { return c.present["image"], c.Refused },
		func(c tomlCase) bool { return c.present["image"] })

	var rules runnersConfig
	readContract(t, runnersConfigFile, &rules)
	pattern := regexp.MustCompile(rules.Image.Pattern)

	declared, absent := 0, 0
	for _, c := range table.Cases {
		if !c.present["image"] {
			continue
		}
		if c.Image == nil {
			absent++
			continue
		}
		declared++
		if !pattern.MatchString(*c.Image) || len(*c.Image) > rules.Image.MaxLength {
			t.Errorf("%s: %s parses out image %q, which %s refuses",
				file, c.Name, *c.Image, runnersConfigFile)
		}
	}
	if declared == 0 || absent == 0 {
		t.Errorf("%s pins %d declared images and %d absent ones; the escape hatch and the "+
			"version-pinned default both need a case", file, declared, absent)
	}

	// A refused image case must be refused for a reason the image rule can
	// see: either the value is not a well-formed reference, or it is not a
	// string at all. Both are in the fixture, and a table that lost the
	// injection case would be a table that stopped testing the thing the rule
	// exists for.
	if !strings.Contains(strings.ToLower(rawString(t, file)), "rm -rf") {
		t.Errorf("%s no longer carries a shell-fragment case; an image reference is a name and "+
			"never a place to hide one", file)
	}
}

// The executable half: ticfac's `[roles.*]` reader over every case document in
// all three tables. The reader owns ONE table family and must be indifferent to
// everything else in a real config — including the tables that carry a
// multi-line string, an inline table, a shell fragment and a `__proto__` key.
func TestTheRolesReaderReadsEveryCaseDocument(t *testing.T) {
	files := []string{"sandbox-image-cases.json", "signal-source-cases.json", "sweep-policy-cases.json"}
	documents := 0
	for _, file := range files {
		var table tomlCaseTable
		readContract(t, file, &table)
		for _, c := range table.Cases {
			documents++
			roles, err := profile.ParseRoles(c.TOML)
			if err != nil {
				t.Errorf("%s/%s: the roles reader refused a whole config document: %v", file, c.Name, err)
				continue
			}
			// Every document in the bundle declares exactly this role, and it
			// is the reader's job to find it and nothing else.
			implement, ok := roles["implement"]
			if !ok {
				t.Errorf("%s/%s: the reader found no [roles.implement] in a document that declares one: %v",
					file, c.Name, roles)
				continue
			}
			if implement.Kind != "claude" {
				t.Errorf("%s/%s: [roles.implement] read as kind %q", file, c.Name, implement.Kind)
			}
			if len(roles) != 1 {
				t.Errorf("%s/%s: the reader read %v; every other table in these documents is somebody else's",
					file, c.Name, roles)
			}
		}
	}
	if documents < 30 {
		t.Errorf("only %d case documents were read; the three tables carry many more", documents)
	}
}

func rawString(t *testing.T, file string) string {
	t.Helper()
	return string(rawContract(t, file))
}

// The signal sources: accepted declarations name the sources they parse out,
// and every refusal is a fail-closed refusal by KEY. Ignoring an unknown key
// is how a typo'd `header` becomes an unauthenticated door, so the table's
// refusals are what this reader counts.
func TestSignalSourceCases(t *testing.T) {
	const file = "signal-source-cases.json"
	var table tomlCaseTable
	readContract(t, file, &table)

	checkTable(t, file, table,
		func(c tomlCase) (bool, bool) { return c.Accepted, c.Refused },
		func(c tomlCase) bool { return c.Sources != nil })

	for _, c := range table.Cases {
		if !c.Accepted {
			continue
		}
		if !isSorted(c.Sources) {
			t.Errorf("%s/%s reports sources %v out of order; two declarations must be reported in one order every time",
				file, c.Name, c.Sources)
		}
		for _, name := range c.Sources {
			if !strings.Contains(c.TOML, name) {
				t.Errorf("%s/%s reports source %q that its own document does not declare", file, c.Name, name)
			}
		}
	}
}

// The sweep policies: same shape, and the same reason for the order — two
// policies due at the same minute must fire in the same order every time.
func TestSweepPolicyCases(t *testing.T) {
	const file = "sweep-policy-cases.json"
	var table tomlCaseTable
	readContract(t, file, &table)

	checkTable(t, file, table,
		func(c tomlCase) (bool, bool) { return c.Accepted, c.Refused },
		func(c tomlCase) bool { return c.Sweeps != nil })

	for _, c := range table.Cases {
		if !c.Accepted {
			continue
		}
		if !isSorted(c.Sweeps) {
			t.Errorf("%s/%s reports sweeps %v out of order; a sweep order that depends on map "+
				"iteration is a different run every time", file, c.Name, c.Sweeps)
		}
		for _, name := range c.Sweeps {
			if !strings.Contains(c.TOML, name) {
				t.Errorf("%s/%s reports sweep %q that its own document does not declare", file, c.Name, name)
			}
		}
	}
}

func isSorted(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] > values[i] {
			return false
		}
	}
	return true
}
