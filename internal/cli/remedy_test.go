package cli

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A remedy the run prints is a command a person pastes. epic-2jn's refusal
// said `ticfac settle 2jn 4mv 33 --release "<who>"`, and that exact line
// failed with "exactly one epic id, tick id and attempt number are required":
// Go's flag package stops at the first positional argument, so every flag
// written after the ids was read as a fourth positional. A remedy that the
// CLI rejects sends the person who followed it to the source code.
//
// This test reads every `ticfac …` command the reconciler and the CLI write
// into their own text — the string literals, concatenations included, of
// internal/reconcile and internal/cli — instantiates its placeholders, and
// hands it to the real CLI's parser (Run, stopped right after each command
// has parsed and validated its arguments). Any printed command the CLI would
// refuse as a usage error fails here, naming where it is printed.

// remedyScanned are the packages whose text is scanned: the reconciler's
// refusals and the CLI's own lines.
var remedyScanned = []string{filepath.Join("..", "reconcile"), "."}

// remedyCommand matches one backticked `ticfac …` command inside a literal.
var remedyCommand = regexp.MustCompile("`(ticfac [^`]+)`")

// releaseCommandSpelledWhole matches the release command spelled as a whole
// literal — the shape the reconciler's helper (tick qxj) spells once and the
// refusals interpolate, no backticks around it in its own spelling. A bare
// mention of the command's name with nothing to instantiate (the settle
// command's own usage refusal) does not match: the format verbs are the shape
// of a command a person can fill in, and the match is anchored to the whole
// literal so the command it instantiates is the complete one.
var releaseCommandSpelledWhole = regexp.MustCompile(`^ticfac settle %s %s %d (?:--run-id %s )?--release "<[^">]*>"$`)

type printedRemedy struct {
	where   string
	command string
}

func TestEveryPrintedRemedyIsACommandTheCLIAccepts(t *testing.T) {
	var remedies []printedRemedy
	for _, dir := range remedyScanned {
		remedies = append(remedies, remediesIn(t, dir)...)
	}
	settles := 0
	for _, remedy := range remedies {
		if strings.HasPrefix(remedy.command, "ticfac settle ") {
			settles++
		}
	}
	if settles < 2 {
		t.Fatalf("the scan found %d printed settle remedies, want the release command's two forms — the "+
			"reconciler's helper spells it once and the refusals interpolate it (tick qxj): the scan is "+
			"broken, not the remedies (%v)", settles, remedies)
	}

	parseOnly = true
	defer func() { parseOnly = false }()
	for _, remedy := range remedies {
		args := remedyArgs(remedy.command)
		if len(args) < 2 {
			continue
		}
		if !parseOnlyCommands[args[1]] {
			t.Errorf("%s prints `%s`, and `ticfac %s` stops nowhere after parsing: teach it parseOnly so this "+
				"test can hold its printed remedies to the parser", remedy.where, remedy.command, args[1])
			continue
		}
		var stderr bytes.Buffer
		if code := Run(args[1:], io.Discard, &stderr); code != 0 {
			t.Errorf("%s prints the remedy `%s`, and the CLI refuses it (exit %d) as %q:\n%s",
				remedy.where, remedy.command, code, strings.Join(args, " "), stderr.String())
		}
	}
}

// remedyArgs instantiates one printed command: format verbs and <placeholders>
// become sample values, and the line is split the way a shell splits it.
func remedyArgs(command string) []string {
	command = regexp.MustCompile(`%d`).ReplaceAllString(command, "33")
	command = regexp.MustCompile(`%[sqv]`).ReplaceAllString(command, "33")
	command = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(command, "someone")
	var args []string
	var current strings.Builder
	quoted, started := false, false
	for _, r := range command {
		switch {
		case r == '"':
			quoted, started = !quoted, true
		case r == ' ' && !quoted:
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if started {
		args = append(args, current.String())
	}
	return args
}

// remediesIn reads every runnable `ticfac …` command in the non-test Go
// source of one package directory. A mention with nothing to instantiate —
// `ticfac status` naming the command in prose — is a name, not a remedy; a
// runnable one carries a format verb or a <placeholder> for the ids it needs.
func remediesIn(t *testing.T, dir string) []printedRemedy {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []printedRemedy
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			text, ok := literalText(expr)
			if !ok {
				return true
			}
			for _, match := range remedyCommand.FindAllStringSubmatch(text, -1) {
				command := match[1]
				if strings.Contains(command, "…") || !strings.ContainsAny(command, "%<") {
					continue
				}
				out = append(out, printedRemedy{where: fset.Position(expr.Pos()).String(), command: command})
			}
			// The release command's own spelling is a remedy like any other,
			// though it carries no backticks of its own: the run's refusals
			// interpolate it as `%s`, and only the whole-literal shape it is
			// spelled in parses as the command it is.
			if whole := releaseCommandSpelledWhole.FindString(text); whole != "" {
				out = append(out, printedRemedy{where: fset.Position(expr.Pos()).String(), command: whole})
			}
			// A literal chain is read whole, once: its parts are not visited
			// again on their own.
			return false
		})
	}
	return out
}

// literalText is the value of a string literal, or of a chain of them joined
// with +, the way a long refusal is written across lines.
func literalText(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := literalText(e.X)
		if !ok {
			return "", false
		}
		right, ok := literalText(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	case *ast.ParenExpr:
		return literalText(e.X)
	}
	return "", false
}
