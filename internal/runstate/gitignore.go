package runstate

import (
	"os"
	"path/filepath"
	"strings"
)

// The `.gitignore` fragment of contracts/ticfac-run-state.json, verbatim.
//
// The rule it encodes is the one thing about `.ticfac/` that a target
// repository must not get wrong: `.ticfac/runs/**` is COMMITTED, because it is
// the run's durable record and durable means pushed on origin. Only the derived
// index and the logs are exhaust. An over-broad `.ticfac/` in someone's
// `.gitignore` is a run that leaves nothing behind, and git would say nothing.
const (
	BeginMarker = "# BEGIN ticfac run state"
	EndMarker   = "# END ticfac run state"
)

// Fragment is the block, marker lines included.
var Fragment = []string{
	BeginMarker,
	"# .ticfac/runs/** is COMMITTED — it is the run's durable record (SPEC 10.4),",
	"# and durable means pushed on origin. Only these two are exhaust:",
	Root + "/.index.json",
	Root + "/logs/",
	EndMarker,
}

// EnsureGitignore installs the fragment in a repository's `.gitignore`,
// replacing an existing block between the markers, and reports whether it wrote
// anything. It is idempotent: a repository already carrying the current
// fragment is left byte for byte alone, because a reconciler that rewrites the
// file on every run puts a diff in front of a person for no reason.
//
// Everything outside the markers is the repository's own and is preserved.
func EnsureGitignore(repoRoot string) (bool, error) {
	path := filepath.Join(repoRoot, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	body := string(raw)
	block := strings.Join(Fragment, "\n")

	updated, replaced := replaceBlock(body, block)
	if !replaced {
		updated = body
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		if updated != "" {
			updated += "\n"
		}
		updated += block + "\n"
	}
	if updated == body {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// replaceBlock swaps whatever sits between the markers for the current
// fragment. An older ticfac's block is not left in place: the fragment is the
// contract's, and a stale `.ticfac/` rule inside the markers ignores the whole
// run state.
func replaceBlock(body, block string) (string, bool) {
	begin := strings.Index(body, BeginMarker)
	if begin < 0 {
		return body, false
	}
	end := strings.Index(body[begin:], EndMarker)
	if end < 0 {
		return body, false
	}
	end += begin + len(EndMarker)
	return body[:begin] + block + body[end:], true
}
