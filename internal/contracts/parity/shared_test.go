package parity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// bundleDir is the vendored bundle. Every reader reads it from here rather
// than from a copy of its own: two copies of a parity fixture is the one
// arrangement guaranteed to defeat it.
func bundleDir(t *testing.T) string {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the vendored contract bundle: %v", err)
	}
	return dir
}

// readContract reads one bundle file and decodes it into value.
func readContract(t *testing.T, name string, value any) {
	t.Helper()
	raw := rawContract(t, name)
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

func rawContract(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(bundleDir(t), name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// parseSchema puts one raw schema through the strict subset. A schema the
// validator cannot express fails HERE, which is what makes the fixture a
// contract rather than a document that looks like one.
func parseSchema(t *testing.T, where string, raw json.RawMessage) *schema.Schema {
	t.Helper()
	s, err := schema.ParseSchema(raw)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	return s
}

func parseDefs(t *testing.T, raw map[string]json.RawMessage) map[string]*schema.Schema {
	t.Helper()
	defs, err := schema.ParseDefs(raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return defs
}

func decodeDocument(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	return value
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readFile is os.ReadFile, named here so a reader does not import os for one
// call.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
