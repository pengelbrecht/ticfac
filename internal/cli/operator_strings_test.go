package cli

// ogv: operator-facing strings name THIS binary's commands. The factory move
// ported ticks' text verbatim and never repointed the pointer, so error and
// prose strings kept telling operators to run 'tk factory setup', 'tk cloud
// run' and friends — commands ticks' pwp deleted from tk and ticfac serves as
// its own 'ticfac factory' / 'ticfac cloud' families. An operator or agent who
// followed one ran a binary this repository does not ship and cleared nothing.
//
// The guard scans every non-test Go source under internal/ and cmd/ for
// STRING LITERALS naming those tk subcommand families, so the next verbatim
// port fails here rather than in front of an operator. It reads literals, not
// bytes: comments may say "tk" (the tracker binary this tool genuinely shells
// out to, and the historical narratives that name what tk once served), and
// test files may assert the negative ('the output does not say tk ...') —
// neither is a pointer an operator can follow. 'tk herd' is deliberately not
// matched: no operator-facing string points at it, and the branch-namespace
// registry in the factory names 'tk herd spawn' as an ACTOR on an operator's
// laptop (who else creates tick/* branches), not as a command to run.
import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// tkFactoryOrCloudPointer matches, inside a string literal's VALUE, a pointer
// at a tk subcommand this binary does not serve: 'tk factory setup',
// "`tk factory deploy`", "tk cloud run <epic>", "tk cloud logs ..." — in any
// quoting an operator might read.
var tkFactoryOrCloudPointer = regexp.MustCompile(`tk (factory|cloud)\b`)

func TestOperatorFacingStringsNameThisBinarysCommands(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
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
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(sources) == 0 {
		t.Fatal("found no Go sources to guard — the scan cannot be silently empty")
	}

	misdirected := 0
	for _, path := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				// Raw strings with newlines parse fine; anything that does not
				// is a parse surprise worth seeing, not a reason to skip.
				t.Errorf("unquote %s: %v", fset.Position(literal.Pos()), err)
				return true
			}
			if where := tkFactoryOrCloudPointer.FindString(value); where != "" {
				misdirected++
				rel, err := filepath.Rel(root, path)
				if err != nil {
					rel = path
				}
				t.Errorf("%s:%d: operator-facing string still points at the ticks CLI (%q) — this binary serves it as 'ticfac %s'",
					rel, fset.Position(literal.Pos()).Line, value, strings.TrimPrefix(where, "tk "))
			}
			return true
		})
	}
}
