// Package schema implements the strict JSON Schema SUBSET the ticks contract
// bundle is written in, ported from ticks' internal/tkcontract/schema.go
// (bundle 3.0.0) so that ticfac can read the bundle without importing a ticks
// Go package — SPEC §3.1: the dependency on ticks is `tk --json` and the
// pinned bundle, never a shared library.
//
// Ten keywords, and no eleventh: $ref (local only), type, required,
// properties, additionalProperties, items, enum, anyOf, description and
// $comment. A keyword outside that set makes ParseSchema FAIL rather than be
// ignored, which is the property that makes a schema in the bundle a contract
// instead of decoration: a validator that silently skips what it does not
// understand lets a schema read as if it asserted something while asserting
// nothing.
//
// The refusal text is part of the port. contracts/job-protocol.json and
// contracts/ticfac-run-state.json pin `expect_error_contains` strings that
// every reader of the bundle must produce, so the message formats below are
// contract surface and not an implementation detail.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Schema is the subset of JSON Schema this repository validates against.
//
// It is deliberately small, and deliberately STRICT about its own size: a
// keyword that is not a field here makes ParseSchema fail. Growing the subset
// is a code change, on purpose — and a change that has to happen on the ticks
// side too, or the two validators stop agreeing about what the bundle means.
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 TypeSet            `json:"type,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	AnyOf                []*Schema          `json:"anyOf,omitempty"`
	Description          string             `json:"description,omitempty"`
	Comment              string             `json:"$comment,omitempty"`
}

// Keywords is the whole subset, in the order the bundle's README lists it. It
// is exported so a reader can assert the size of what it implements rather
// than trusting a comment.
var Keywords = []string{
	"$ref", "type", "required", "properties", "additionalProperties",
	"items", "enum", "anyOf", "description", "$comment",
}

// TypeSet is JSON Schema's `type`, which is either one name or a list of them.
type TypeSet []string

func (ts *TypeSet) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*ts = TypeSet{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf(`"type" must be a string or an array of strings`)
	}
	*ts = TypeSet(many)
	return nil
}

func (ts TypeSet) MarshalJSON() ([]byte, error) {
	if len(ts) == 1 {
		return json.Marshal(ts[0])
	}
	return json.Marshal([]string(ts))
}

var knownTypes = map[string]bool{
	"object": true, "array": true, "string": true,
	"number": true, "integer": true, "boolean": true, "null": true,
}

// ParseSchema decodes one schema, rejecting any keyword the validator does not
// implement and any type name it does not know.
func ParseSchema(data []byte) (*Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Schema
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if err := s.check(""); err != nil {
		return nil, err
	}
	return &s, nil
}

// check walks a decoded schema for type names the validator cannot enforce.
// Unknown *keywords* are already gone by then (DisallowUnknownFields), so this
// is only about the values.
func (s *Schema) check(path string) error {
	if s == nil {
		return nil
	}
	for _, name := range s.Type {
		if !knownTypes[name] {
			return fmt.Errorf("schema %s: unknown type %q", pathOrRoot(path), name)
		}
	}
	if s.Ref != "" && !strings.HasPrefix(s.Ref, "#/$defs/") {
		return fmt.Errorf("schema %s: only #/$defs/<name> refs are supported, got %q", pathOrRoot(path), s.Ref)
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.Properties[name].check(path + "." + name); err != nil {
			return err
		}
	}
	if err := s.Items.check(path + "[]"); err != nil {
		return err
	}
	for i, alt := range s.AnyOf {
		if err := alt.check(fmt.Sprintf("%s.anyOf[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func pathOrRoot(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}

// Validate checks value against s, resolving $ref against defs. It returns
// every violation it finds rather than the first, so a drifted document is
// reported in one pass instead of one field per test run.
func Validate(s *Schema, defs map[string]*Schema, value any) []string {
	var errs []string
	validate(s, defs, value, "$", &errs)
	return errs
}

func validate(s *Schema, defs map[string]*Schema, value any, path string, errs *[]string) {
	if s == nil {
		return
	}
	// $ref applies ALONGSIDE its siblings, as JSON Schema 2020-12 specifies —
	// it is not a replacement for the schema that carries it.
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/$defs/")
		target, ok := defs[name]
		if !ok {
			*errs = append(*errs, fmt.Sprintf("%s: unresolvable $ref %q", path, s.Ref))
			return
		}
		validate(target, defs, value, path, errs)
	}

	if len(s.Type) > 0 && !matchesAnyType(s.Type, value) {
		*errs = append(*errs, fmt.Sprintf("%s: expected type %s, got %s",
			path, strings.Join(s.Type, "|"), jsonTypeOf(value)))
		return
	}

	if len(s.Enum) > 0 && !containsValue(s.Enum, value) {
		*errs = append(*errs, fmt.Sprintf("%s: %v is not one of the permitted values %v", path, value, s.Enum))
	}

	if len(s.AnyOf) > 0 {
		matched := false
		for _, alt := range s.AnyOf {
			if len(Validate(alt, defs, value)) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			*errs = append(*errs, fmt.Sprintf("%s: value matches none of the anyOf alternatives", path))
		}
	}

	switch v := value.(type) {
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := v[name]; !ok {
				*errs = append(*errs, fmt.Sprintf("%s: missing required property %q", path, name))
			}
		}
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sub, declared := s.Properties[name]
			if !declared {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					*errs = append(*errs, fmt.Sprintf("%s: unexpected property %q", path, name))
				}
				continue
			}
			validate(sub, defs, v[name], path+"."+name, errs)
		}
	case []any:
		if s.Items != nil {
			for i, item := range v {
				validate(s.Items, defs, item, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	}
}

func matchesAnyType(types TypeSet, value any) bool {
	for _, name := range types {
		if matchesType(name, value) {
			return true
		}
	}
	return false
}

func matchesType(name string, value any) bool {
	switch name {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		f, ok := value.(float64)
		return ok && f == math.Trunc(f)
	}
	return false
}

func jsonTypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", value)
}

func containsValue(allowed []any, value any) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

// ParseDefs parses a `$defs` map of raw schemas, naming whichever one fails.
func ParseDefs(raw map[string]json.RawMessage) (map[string]*Schema, error) {
	defs := make(map[string]*Schema, len(raw))
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s, err := ParseSchema(raw[name])
		if err != nil {
			return nil, fmt.Errorf("$defs.%s: %w", name, err)
		}
		defs[name] = s
	}
	return defs, nil
}
