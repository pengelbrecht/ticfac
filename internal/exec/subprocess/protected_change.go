package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The protected-change proposal (epic-v5t, 2026-10-07: ticks tda and yck).
//
// The worker boundary (OutsideBoundary) refuses every write under `.tick/`
// but the few files it exempts, and it is right to: the run's routing
// (`.tick/runners.cloud.toml`, `.tick/runners.local.toml`) is an authority a
// worker must not rewrite under the run that reads it. That left one kind of
// deliverable no worker could ever make. Tick tda's acceptance put two named
// configs in runners.cloud.toml; the worker parked the block in testdata and
// filed a high-severity finding; the absorption made it tick yck, whose only
// deliverable was the protected edit; every worker on yck answered BLOCKED
// with a `sed … >> .tick/runners.cloud.toml` for the operator, and the run
// ended failed, waiting for a person.
//
// So a worker PROPOSES the change, typed, as a v2 finding's
// `protected_change` key, and the run applies it itself, onto the epic
// branch, as one labelled commit — after the close-out's own reads, so it
// can never change the routing or the evidence authority of the run that
// applies it — and lists it, with its diff, in the epic PR for the person
// who merges (internal/reconcile/protected_changes.go):
//
//	{"kind": "defect", "title": "…", "severity": "high",
//	 "protected_change": {"path": ".tick/runners.cloud.toml", "append": "[configs.x]\n…"}}
//
// `content` replaces the file whole; `append` adds to its end. Exactly one.
// The boundary itself is unchanged: a worker that writes the file in its own
// commits is still refused.

// MaxProtectedChange bounds one proposal's text, in bytes.
const MaxProtectedChange = 65536

// ProtectedChange is one proposed change to one protected file.
type ProtectedChange struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Append  string `json:"append,omitempty"`
}

// String names the change for a record or a message.
func (c ProtectedChange) String() string {
	if c.Append != "" {
		return fmt.Sprintf("an append to %s", c.Path)
	}
	return fmt.Sprintf("a new %s", c.Path)
}

// Text is the proposal's text: the new content or the appended lines.
func (c ProtectedChange) Text() string {
	if c.Append != "" {
		return c.Append
	}
	return c.Content
}

// Applied is the file's new content given its current content: the proposal
// itself for a replacement, the current file with the lines appended (on a
// line of their own) for an append. An append the file already ends with is
// already applied, so a resumed run writes nothing twice.
func (c ProtectedChange) Applied(current string) string {
	if c.Append == "" {
		return c.Content
	}
	add := c.Append
	if !strings.HasSuffix(add, "\n") {
		add += "\n"
	}
	if strings.HasSuffix(current, add) {
		return current
	}
	if current != "" && !strings.HasSuffix(current, "\n") {
		current += "\n"
	}
	return current + add
}

// Validate is the proposal's context-free shape: a path the channel takes,
// and exactly one of content or append, text a file can hold.
func (c ProtectedChange) Validate() error {
	path := strings.TrimPrefix(strings.TrimSpace(c.Path), "./")
	if path == "" {
		return fmt.Errorf("names no path")
	}
	if path != c.Path {
		return fmt.Errorf("path %q is not a clean repository path (write %q)", c.Path, path)
	}
	if !ProposableProtectedPath(path) {
		if !OutsideBoundary(path) && !CloudBoundaryRefuses(path) {
			return fmt.Errorf("%s is not a protected path: commit the change to it yourself", path)
		}
		return fmt.Errorf("%s is protected and not a configuration file this channel changes: only a file "+
			"directly in .tick/ (%s) is proposed here; a tracker record is a tracker_edit, and the run's own "+
			"state under .ticfac/ is never a worker's", path, strings.Join(proposableExamples(), ", "))
	}
	switch {
	case c.Content != "" && c.Append != "":
		return fmt.Errorf("carries both content and append; a change is one of them")
	case c.Content == "" && c.Append == "":
		return fmt.Errorf("carries neither content (the whole new file) nor append (lines to add to its end)")
	}
	text := c.Text()
	if len(text) > MaxProtectedChange {
		return fmt.Errorf("the text is %d bytes; at most %d", len(text), MaxProtectedChange)
	}
	for _, r := range text {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7f {
			return fmt.Errorf("the text carries a control character (%U)", r)
		}
	}
	return nil
}

// CloudBoundaryRefuses is the CLOUD substrate's boundary: the container's
// pre-commit hook (image/worker.sh) and the cloud collect
// (cloudflare/src/worker-collect.ts, boundary_files) refuse every path under
// `.tick/` — the files OutsideBoundary exempts (runners.toml, config.md,
// learnings.md) included (tick 9sy). A worker on the cloud can write none of
// them, so the protected-change channel must take them all there.
func CloudBoundaryRefuses(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	return path == ".tick" || strings.HasPrefix(path, ".tick/")
}

// ProposableProtectedPath says whether a path is one a worker may propose a
// change to through the run: a file DIRECTLY in .tick/ — the run's and the
// tracker's configuration. Every such file is refused by some substrate's
// boundary: the cloud's refuses all of them (CloudBoundaryRefuses), the local
// one all but the few it exempts (OutsideBoundary) — runners.cloud.toml and
// runners.local.toml everywhere, runners.toml's [testing.commands] on the
// cloud (epic ex6's 2pn). A tracker record (.tick/issues/…, .tick/activity/…)
// has its own channel, and the run's state (.ticfac/) is never a worker's to
// propose.
func ProposableProtectedPath(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	if !CloudBoundaryRefuses(path) && !OutsideBoundary(path) {
		return false
	}
	rest, ok := strings.CutPrefix(path, ".tick/")
	return ok && rest != "" && rest != "." && rest != ".." && !strings.Contains(rest, "/")
}

// UnwritableOn says whether a worker on the substrate can not write a
// proposable path in its own commits: on the cloud none of them, locally
// only those OutsideBoundary refuses.
func UnwritableOn(path string, cloud bool) bool {
	if !ProposableProtectedPath(path) {
		return false
	}
	return cloud || OutsideBoundary(path)
}

// proposableExamples names the protected configuration this repository's
// layout has, for a message.
func proposableExamples() []string {
	return []string{".tick/runners.cloud.toml", ".tick/runners.local.toml", ".tick/runners.toml (on the cloud)"}
}

// protectedPathMention finds a `.tick/<file>` mention in prose.
var protectedPathMention = regexp.MustCompile(`\.tick/[A-Za-z0-9._-]+/?`)

// ProtectedPathsIn is every path a text names that a worker on the substrate
// (cloud or not) may not write and the protected-change channel takes, in
// order, each once. Trailing punctuation a sentence puts after a path is not
// part of it.
func ProtectedPathsIn(text string, cloud bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, match := range protectedPathMention.FindAllString(text, -1) {
		if strings.HasSuffix(match, "/") {
			continue // a directory under .tick/: records, never configuration
		}
		path := strings.TrimRight(match, ".")
		if !seen[path] && UnwritableOn(path, cloud) {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// readProtectedChange decodes one proposal strictly — it is a WRITE, so a
// key this reader does not know is refused rather than folded — and holds it
// to Validate.
func readProtectedChange(raw json.RawMessage) (ProtectedChange, error) {
	var change ProtectedChange
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return change, fmt.Errorf("is null; a protected change is an object {\"path\", \"content\" or \"append\"}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&change); err != nil {
		return change, fmt.Errorf("is not an object {\"path\", \"content\" or \"append\"}: %v", err)
	}
	if err := change.Validate(); err != nil {
		return change, err
	}
	return change, nil
}
