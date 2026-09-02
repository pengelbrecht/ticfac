package parity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/credential-ownership.json — EXECUTABLE.
//
// Which product owns each credential type, the `~/.ticfacrc` key set with its
// redacted example, and the stop / cost / security lifecycle rules. This one
// is ticfac's own contract: ticks issues NO execution credentials, and every
// key in this file is written and read by ticfac. It is here so that the
// division cannot drift — a key ticfac starts writing into `~/.ticksrc`, or a
// credential ticks starts issuing, is a boundary moving with nothing failing.
//
// Executable because the file carries a schema in the strict subset, one
// golden document and five negative documents that pin the exact refusal they
// expect. The refusal TEXT is the parity: a validator that has quietly stopped
// checking the thing a case was written about still fails "something failed",
// and would pass a negative that only asserted an error.

const credentialOwnershipFile = "credential-ownership.json"

type credentialOwnership struct {
	SchemaVersion int    `json:"schema_version"`
	Contract      string `json:"contract"`
	File          struct {
		Path         string `json:"path"`
		Mode         string `json:"mode"`
		Format       string `json:"format"`
		Encoding     string `json:"encoding"`
		UnknownLines string `json:"unknown_lines"`
		AtomicWrites string `json:"atomic_writes"`
	} `json:"file"`
	Ownership []struct {
		CredentialType string   `json:"credential_type"`
		Owner          string   `json:"owner"`
		FileKeys       []string `json:"file_keys"`
	} `json:"ownership"`
	Ticks struct {
		ExecutionCredentials []string `json:"execution_credentials"`
		BoardSyncFile        string   `json:"board_sync_file"`
		Git                  string   `json:"git"`
	} `json:"ticks"`
	Keys []struct {
		Name           string   `json:"name"`
		CredentialType string   `json:"credential_type"`
		Secret         bool     `json:"secret"`
		StoredIn       []string `json:"stored_in"`
	} `json:"keys"`
	Schema       json.RawMessage   `json:"schema"`
	ValidExample map[string]string `json:"valid_example"`
	Invalid      []struct {
		Name                string          `json:"name"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
	Lifecycle struct {
		Stop struct {
			RevokeBeforeStop             bool   `json:"revoke_before_stop"`
			RefuseIssueBeforeEveryBoot   bool   `json:"refuse_issue_before_every_boot"`
			CancelledHandleCannotReissue bool   `json:"cancelled_handle_cannot_reissue"`
			Rule                         string `json:"rule"`
		} `json:"stop"`
		Security struct {
			NeverPrintTokens bool `json:"never_print_tokens"`
		} `json:"security"`
	} `json:"lifecycle"`
}

func TestCredentialSchemaAdmitsItsGoldenDocument(t *testing.T) {
	var c credentialOwnership
	readContract(t, credentialOwnershipFile, &c)

	if c.Contract == "" || c.SchemaVersion != 1 {
		t.Fatalf("the contract does not identify itself: %q v%d", c.Contract, c.SchemaVersion)
	}
	s := parseSchema(t, "schema", c.Schema)

	document := map[string]any{}
	for k, v := range c.ValidExample {
		document[k] = v
	}
	if errs := schema.Validate(s, nil, document); len(errs) != 0 {
		t.Errorf("the contract's own valid example is refused by its own schema:\n  %s",
			strings.Join(errs, "\n  "))
	}
	if len(c.ValidExample) == 0 {
		t.Error("there is no valid example, so nothing has ever been admitted")
	}
}

// Each negative document is refused, and refused for the reason it names.
func TestCredentialSchemaRefusesEveryNegativeDocument(t *testing.T) {
	var c credentialOwnership
	readContract(t, credentialOwnershipFile, &c)
	s := parseSchema(t, "schema", c.Schema)

	if len(c.Invalid) == 0 {
		t.Fatal("no negative example: a validator nobody has seen refuse anything is not known to refuse anything")
	}
	for _, bad := range c.Invalid {
		if bad.ExpectErrorContains == "" {
			t.Errorf("%s: pins no expected refusal; \"something failed\" is satisfied by a validator that stopped checking", bad.Name)
			continue
		}
		errs := schema.Validate(s, nil, decodeDocument(t, bad.Document))
		if len(errs) == 0 {
			t.Errorf("%s: accepted a document the contract says is invalid (%s)", bad.Name, bad.Why)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), bad.ExpectErrorContains) {
			t.Errorf("%s: refused with\n  %s\nthe contract expects\n  %s",
				bad.Name, strings.Join(errs, "\n  "), bad.ExpectErrorContains)
		}
	}
}

// The file's shape is CLOSED, which is the whole reason a misspelled key is
// caught: a typo silently persisted is a credential the operator believes they
// set.
func TestTheCredentialFileIsClosed(t *testing.T) {
	var c credentialOwnership
	readContract(t, credentialOwnershipFile, &c)
	s := parseSchema(t, "schema", c.Schema)

	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		t.Fatal("the credential file's schema is open; a misspelled key would be accepted in silence")
	}
	if c.File.Path == "" || c.File.Mode == "" {
		t.Error("the contract does not say where the file is or what mode it must have")
	}
	if c.File.Mode != "0600" {
		t.Errorf("mode %q: a file holding tokens is readable by its owner and nobody else", c.File.Mode)
	}
	if c.File.AtomicWrites == "" {
		t.Error("the contract does not say how the file is written; a torn write loses every credential at once")
	}
}

// The division of ownership, which is the contract's title: ticks issues no
// execution credentials, every key here belongs to a declared credential type,
// and no key is claimed by two types.
func TestEveryKeyBelongsToExactlyOneOwnedCredentialType(t *testing.T) {
	var c credentialOwnership
	readContract(t, credentialOwnershipFile, &c)

	if len(c.Ticks.ExecutionCredentials) != 0 {
		t.Errorf("ticks issues execution credentials %v; the whole division is that it issues none",
			c.Ticks.ExecutionCredentials)
	}
	if c.Ticks.BoardSyncFile == "" || c.Ticks.BoardSyncFile == c.File.Path {
		t.Errorf("ticks' own file is %q and ticfac's is %q; one file for two owners is the drift this contract prevents",
			c.Ticks.BoardSyncFile, c.File.Path)
	}

	types := map[string]string{}
	claimed := map[string]string{}
	for _, o := range c.Ownership {
		if o.Owner != "ticfac" {
			t.Errorf("credential type %q is owned by %q; every credential in this file is ticfac's",
				o.CredentialType, o.Owner)
		}
		if _, ok := types[o.CredentialType]; ok {
			t.Errorf("credential type %q is declared twice", o.CredentialType)
		}
		types[o.CredentialType] = o.Owner
		for _, key := range o.FileKeys {
			if other, ok := claimed[key]; ok {
				t.Errorf("key %q is claimed by both %q and %q", key, other, o.CredentialType)
			}
			claimed[key] = o.CredentialType
		}
	}

	s := parseSchema(t, "schema", c.Schema)
	for _, k := range c.Keys {
		if _, ok := types[k.CredentialType]; !ok {
			t.Errorf("key %q has credential type %q, which no ownership row declares", k.Name, k.CredentialType)
		}
		if claimed[k.Name] == "" {
			t.Errorf("key %q is declared and no ownership row lists it", k.Name)
		}
		if _, ok := s.Properties[k.Name]; !ok {
			t.Errorf("key %q is declared and the schema does not carry it", k.Name)
		}
		if len(k.StoredIn) == 0 {
			t.Errorf("key %q says nothing about where it is stored", k.Name)
		}
		for _, where := range k.StoredIn {
			if where == c.Ticks.BoardSyncFile {
				t.Errorf("key %q is stored in ticks' own file", k.Name)
			}
		}
	}
	for name := range s.Properties {
		if claimed[name] == "" {
			t.Errorf("the schema carries %q and no ownership row claims it", name)
		}
	}
}

// The lifecycle rules Appendix A #1 depends on. They are pinned in two
// contracts on purpose — lifecycle-invariants.json's A1 names this file as
// where the rule already lives — so a relaxation here has to be a relaxation
// there too.
func TestTheStopRuleIsADurableRefusalToIssue(t *testing.T) {
	var c credentialOwnership
	readContract(t, credentialOwnershipFile, &c)

	stop := c.Lifecycle.Stop
	if !stop.RefuseIssueBeforeEveryBoot {
		t.Error("a stop that is not checked before every boot is undone by the next reboot")
	}
	if !stop.RevokeBeforeStop {
		t.Error("a container torn down before its credential is revoked can spend on the way out")
	}
	if !stop.CancelledHandleCannotReissue {
		t.Error("a cancelled handle that can reissue is a cancellation that did not cancel")
	}
	if !strings.Contains(strings.ToLower(stop.Rule), "durable") {
		t.Errorf("the stop rule no longer says it is durable: %q", stop.Rule)
	}
	if !c.Lifecycle.Security.NeverPrintTokens {
		t.Error("a token printed once is a token in a log")
	}

	// The redacted example is the operator-facing half: a fixture carrying a
	// real-looking token teaches the wrong thing and eventually IS one.
	for name, value := range c.ValidExample {
		for _, k := range c.Keys {
			if k.Name == name && k.Secret && !strings.Contains(value, "redacted") {
				t.Errorf("the example value for the secret key %q is not redacted: %q", name, value)
			}
		}
	}
}
