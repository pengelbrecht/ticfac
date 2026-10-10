package cli

// Tick 47j: the dashboard's recent-activity section narrates the feed in
// the operator's own words, never the raw stage — so the one way it can go
// wrong silently is a new Stage constant nobody taught a sentence. This
// guard scans internal/reconcile's and internal/runfeed's own non-test
// sources for every `Stage...` constant's declared VALUE (resolving the
// handful that alias a runfeed constant, e.g. reconcile.StageRunFinished =
// runfeed.StageRunFinished) and fails if one is missing from feedSentences
// — a nil entry is a deliberate mechanic (pushed, push_queued,
// policy_stated, cleaned_up); an absent one is the bug this guards against.
import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseStageConsts reads every `Stage... = ...` constant in dir's non-test
// Go sources. seed resolves a value that is itself another package's
// constant (a selector expression) rather than a string literal — the
// caller parses that package first and passes its own result in.
func parseStageConsts(t *testing.T, dir string, seed map[string]string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	values := map[string]string{}
	seen := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		seen++
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, ident := range vs.Names {
					if !strings.HasPrefix(ident.Name, "Stage") {
						continue
					}
					value, ok := stageConstValue(vs.Values[i], seed)
					if !ok {
						t.Fatalf("%s: %s is a Stage constant whose value this guard cannot read (not a string "+
							"literal, and not a known cross-package alias) — teach parseStageConsts its shape "+
							"before trusting the sentence map is complete", filepath.Join(filepath.Base(dir), name),
							ident.Name)
					}
					values[ident.Name] = value
				}
			}
		}
	}
	if seen == 0 {
		t.Fatalf("found no Go sources in %s — the scan cannot be silently empty", dir)
	}
	return values
}

// stageConstValue reads one Stage constant's declared value: a string
// literal, or a selector into a package whose constants seed already
// carries (by name — the alias and the aliased constant share one, e.g.
// StageRunFinished).
func stageConstValue(expr ast.Expr, seed map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return v, true
	case *ast.SelectorExpr:
		v, ok := seed[e.Sel.Name]
		return v, ok
	default:
		return "", false
	}
}

// TestEveryFeedStageHasASentence is the completeness guard the tick's
// acceptance names: it fails the moment a new Stage constant lands in
// internal/reconcile or internal/runfeed without an entry in feedSentences
// (feed_sentences.go) — present as a sentence, or present as nil to say
// "this one is a pure mechanic, left off the dashboard's recent-activity
// section on purpose".
func TestEveryFeedStageHasASentence(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runfeedValues := parseStageConsts(t, filepath.Join(wd, "..", "runfeed"), nil)
	reconcileValues := parseStageConsts(t, filepath.Join(wd, "..", "reconcile"), runfeedValues)

	all := map[string]string{}
	for name, value := range runfeedValues {
		all[name] = value
	}
	for name, value := range reconcileValues {
		all[name] = value
	}
	if len(all) < 50 {
		t.Fatalf("found only %d Stage constants across internal/reconcile and internal/runfeed — "+
			"the scan likely missed a file", len(all))
	}

	missing := []string{}
	for name, value := range all {
		if _, ok := feedSentences[value]; !ok {
			missing = append(missing, fmt.Sprintf("%s (%q)", name, value))
		}
	}
	if len(missing) > 0 {
		t.Errorf("feedSentences (feed_sentences.go) has no entry — sentence, or nil to filter it — for:\n  %s",
			strings.Join(missing, "\n  "))
	}
}
