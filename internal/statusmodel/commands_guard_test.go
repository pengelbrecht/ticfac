package statusmodel

// l1t: commands.go is the one spelling of every clearing command the model
// names — the settle, the triage, the resume — and the current-run mirror
// attentionCommand broke that by formatting the empty-run-id settle command
// inline, a second spelling of the same sentence in the same package. If
// SettleCommand's wording ever changed, the row for a tick the current run
// held would drift from the header's needs-you command, the exact class of
// disagreement tick eli fixed for prior holds.
//
// The guard scans the package's non-test Go sources for STRING LITERALS
// naming one of those commands, so the next inline spelling fails here
// rather than drifting from the header in front of an operator. It reads
// literals, not bytes: comments may quote the commands (commands.go's own
// doc does), and test files may assert the exact wording — neither is a
// second spelling a renderer reads. The scan is per package, not per
// repository: the reconciler's prose and the CLI's usage text tell their
// own stories and answer to their own guards.
import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// clearingCommandWord matches a literal whose VALUE names one of the
// clearing commands — anywhere in it, because prose that embeds the command
// embeds the spelling too ("release it with ticfac settle ..."). The branch
// namespace (`ticfac/run-...`) and the schema id (`ticfac.status.v1`) share
// the prefix but not the space, so they never match.
var clearingCommandWord = regexp.MustCompile(`ticfac (settle|triage|run-epic|run)\b`)

func TestEveryClearingCommandInTheModelIsSpelledOnce(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		sources = append(sources, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(sources) == 0 {
		t.Fatal("found no Go sources to guard — the scan cannot be silently empty")
	}

	for _, path := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("unquote %s:%d: %v", rel, fset.Position(literal.Pos()).Line, err)
				return true
			}
			if clearingCommandWord.MatchString(value) && rel != "commands.go" {
				t.Errorf("%s:%d: a clearing command is spelled outside commands.go (%q) — call the one spelling there, so a wording change cannot drift a row from the header",
					rel, fset.Position(literal.Pos()).Line, value)
			}
			return true
		})
	}
}
