package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// The property the whole port exists for: a keyword outside the subset makes a
// schema fail to PARSE. A validator that ignored it would let a schema in the
// bundle read as if it constrained something no reader checks, which is the
// failure contracts/README.md names ("a copied JSON file without an executable
// check is not a contract").
func TestUnknownKeywordIsAParseError(t *testing.T) {
	for _, keyword := range []string{
		`"minLength": 3`,
		`"pattern": "^a+$"`,
		`"oneOf": []`,
		`"allOf": []`,
		`"format": "uri"`,
		`"minimum": 1`,
		`"$defs": {}`,
		`"additionalItems": false`,
	} {
		body := []byte(`{"type": "string", ` + keyword + `}`)
		if _, err := ParseSchema(body); err == nil {
			t.Errorf("ParseSchema accepted %s; the subset is ten keywords and no eleventh", keyword)
		}
	}
}

func TestTheSubsetIsTenKeywords(t *testing.T) {
	if len(Keywords) != 10 {
		t.Fatalf("the subset declares %d keywords, want 10: %v", len(Keywords), Keywords)
	}
	// Each declared keyword parses, which is what makes the list a claim about
	// the code rather than a comment.
	bodies := map[string]string{
		"$ref":                 `{"$ref": "#/$defs/thing"}`,
		"type":                 `{"type": "string"}`,
		"required":             `{"type": "object", "required": ["a"]}`,
		"properties":           `{"type": "object", "properties": {"a": {"type": "string"}}}`,
		"additionalProperties": `{"type": "object", "additionalProperties": false}`,
		"items":                `{"type": "array", "items": {"type": "string"}}`,
		"enum":                 `{"enum": ["a", "b"]}`,
		"anyOf":                `{"anyOf": [{"type": "string"}, {"type": "null"}]}`,
		"description":          `{"type": "string", "description": "a thing"}`,
		"$comment":             `{"type": "string", "$comment": "why"}`,
	}
	for _, keyword := range Keywords {
		body, ok := bodies[keyword]
		if !ok {
			t.Fatalf("Keywords names %q with no example here", keyword)
		}
		if _, err := ParseSchema([]byte(body)); err != nil {
			t.Errorf("%s: %v", keyword, err)
		}
	}
}

func TestUnknownTypeNameIsRefused(t *testing.T) {
	if _, err := ParseSchema([]byte(`{"type": "date"}`)); err == nil {
		t.Error("ParseSchema accepted an unknown type name")
	}
	if _, err := ParseSchema([]byte(`{"type": ["string", "date"]}`)); err == nil {
		t.Error("ParseSchema accepted an unknown type name inside a type list")
	}
	nested := `{"type":"object","properties":{"a":{"type":"array","items":{"type":"date"}}}}`
	if _, err := ParseSchema([]byte(nested)); err == nil {
		t.Error("ParseSchema accepted an unknown type name nested under items")
	}
}

func TestOnlyLocalDefsRefsAreSupported(t *testing.T) {
	if _, err := ParseSchema([]byte(`{"$ref": "other.json#/$defs/thing"}`)); err == nil {
		t.Error("ParseSchema accepted a cross-file $ref; the subset has none")
	}
	if _, err := ParseSchema([]byte(`{"$ref": "#/definitions/thing"}`)); err == nil {
		t.Error("ParseSchema accepted a non-$defs pointer")
	}
}

func TestTypeIsAStringOrAnArrayOfStrings(t *testing.T) {
	if _, err := ParseSchema([]byte(`{"type": 3}`)); err == nil {
		t.Error("ParseSchema accepted a numeric type")
	}
	s, err := ParseSchema([]byte(`{"type": ["string", "null"]}`))
	if err != nil {
		t.Fatalf("a type list must parse: %v", err)
	}
	if len(s.Type) != 2 {
		t.Errorf("type = %v, want two names", s.Type)
	}
}

func mustParse(t *testing.T, body string) *Schema {
	t.Helper()
	s, err := ParseSchema([]byte(body))
	if err != nil {
		t.Fatalf("parse %s: %v", body, err)
	}
	return s
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

// The refusal TEXT is contract surface: contracts/job-protocol.json and
// contracts/ticfac-run-state.json pin `expect_error_contains` strings that
// every reader of the bundle has to produce. A reworded message here is a
// silent break of the fixtures, so the formats are asserted directly.
func TestRefusalTextIsTheFormTheFixturesPin(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		doc    string
		want   string
	}{
		{
			"missing required property",
			`{"type":"object","required":["credentials"]}`,
			`{}`,
			`missing required property "credentials"`,
		},
		{
			"unexpected property",
			`{"type":"object","additionalProperties":false,"properties":{}}`,
			`{"backend":"x"}`,
			`$: unexpected property "backend"`,
		},
		{
			"wrong type",
			`{"type":"object","properties":{"handle":{"type":"object"}}}`,
			`{"handle":"a string"}`,
			`$.handle: expected type object, got string`,
		},
		{
			"enum",
			`{"type":"object","properties":{"role":{"enum":["implement-tick"]}}}`,
			`{"role":"refactor-everything"}`,
			`$.role: refactor-everything is not one of the permitted values`,
		},
		{
			"anyOf",
			`{"type":"object","properties":{"credentials":{"type":"object","properties":{"source":{"anyOf":[{"type":"string"},{"type":"null"}]}}}}}`,
			`{"credentials":{"source":{}}}`,
			`$.credentials.source: value matches none of the anyOf alternatives`,
		},
	}
	for _, c := range cases {
		errs := Validate(mustParse(t, c.schema), nil, decode(t, c.doc))
		joined := strings.Join(errs, "\n")
		if !strings.Contains(joined, c.want) {
			t.Errorf("%s: errors %q do not contain %q", c.name, joined, c.want)
		}
	}
}

// Every violation, not the first: a drifted document is reported in one pass.
func TestValidateReportsEveryViolation(t *testing.T) {
	s := mustParse(t, `{"type":"object","required":["a","b"],"additionalProperties":false,"properties":{"a":{"type":"string"}}}`)
	errs := Validate(s, nil, decode(t, `{"c": 1}`))
	if len(errs) != 3 {
		t.Errorf("got %d violations, want 3 (two missing, one unexpected): %v", len(errs), errs)
	}
}

// $ref applies ALONGSIDE its siblings, as JSON Schema 2020-12 specifies. That
// is what lets a record be declared as "a tick, plus a required key" without
// copying the definition and letting the copy drift.
func TestRefAppliesAlongsideItsSiblings(t *testing.T) {
	defs := map[string]*Schema{
		"tick": mustParse(t, `{"type":"object","required":["id"]}`),
	}
	s := mustParse(t, `{"$ref":"#/$defs/tick","required":["action"]}`)

	if errs := Validate(s, defs, decode(t, `{"id":"mrq","action":"start"}`)); len(errs) != 0 {
		t.Errorf("a document satisfying both halves was refused: %v", errs)
	}
	errs := Validate(s, defs, decode(t, `{"id":"mrq"}`))
	if len(errs) != 1 || !strings.Contains(errs[0], `missing required property "action"`) {
		t.Errorf("the sibling `required` was not applied: %v", errs)
	}
	errs = Validate(s, defs, decode(t, `{"action":"start"}`))
	if len(errs) != 1 || !strings.Contains(errs[0], `missing required property "id"`) {
		t.Errorf("the $ref target was not applied: %v", errs)
	}
}

func TestUnresolvableRefIsAViolationNotASkip(t *testing.T) {
	s := mustParse(t, `{"$ref":"#/$defs/absent"}`)
	errs := Validate(s, map[string]*Schema{}, decode(t, `{}`))
	if len(errs) != 1 || !strings.Contains(errs[0], "unresolvable $ref") {
		t.Errorf("an unresolvable $ref must be reported, got %v", errs)
	}
}

// integer is a number with no fractional part, which is how encoding/json
// hands every JSON number over.
func TestIntegerIsANumberWithoutAFraction(t *testing.T) {
	s := mustParse(t, `{"type":"integer"}`)
	if errs := Validate(s, nil, decode(t, `3`)); len(errs) != 0 {
		t.Errorf("3 is an integer: %v", errs)
	}
	errs := Validate(s, nil, decode(t, `3.5`))
	if len(errs) != 1 || !strings.Contains(errs[0], "expected type integer, got number") {
		t.Errorf("3.5 is not an integer: %v", errs)
	}
}

// An open record accepts an added field; a closed one refuses it. Both spellings
// are in the bundle on purpose (contracts/README.md), so both are exercised.
func TestAdditionalPropertiesIsOnlyClosedWhenItSaysSo(t *testing.T) {
	open := mustParse(t, `{"type":"object","properties":{"a":{"type":"string"}}}`)
	if errs := Validate(open, nil, decode(t, `{"a":"x","added":1}`)); len(errs) != 0 {
		t.Errorf("an open record refused an added field: %v", errs)
	}
	closed := mustParse(t, `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string"}}}`)
	if errs := Validate(closed, nil, decode(t, `{"a":"x","added":1}`)); len(errs) != 1 {
		t.Errorf("a closed record accepted an added field: %v", errs)
	}
}

func TestParseDefsNamesTheDefThatFails(t *testing.T) {
	raw := map[string]json.RawMessage{
		"good": json.RawMessage(`{"type":"string"}`),
		"bad":  json.RawMessage(`{"type":"string","pattern":"^a$"}`),
	}
	_, err := ParseDefs(raw)
	if err == nil || !strings.Contains(err.Error(), "$defs.bad") {
		t.Errorf("ParseDefs must name the failing def, got %v", err)
	}
}
