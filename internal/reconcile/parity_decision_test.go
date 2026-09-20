package reconcile

// short: reads and hashes files only — no repository is built and no process
// is spawned — and the per-tick gate is exactly where one implementation of
// the reconciler changing a recorded behaviour without its decision must be
// caught.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The parity record (tick sz0): decisions/reconciler-parity.json is the
// written statement of every intentional semantic difference between the Go
// reconciler (this package) and the Workflow reconciler
// (cloudflare/src/epic-reconciler.ts). Two implementations of one thing with
// no statement of where they may differ is how drift happens — the same
// subject the vendored contract bundle (internal/contracts) and the vendored
// image context (internal/sandboxpin) already answer with a pin and a check.
//
// Each decision is pinned to the code regions that implement it on each side
// with marker comments bracketing the recorded behaviour:
//
//	reconciler-decision:<id>:begin:<region>
//	... the behaviour the decision records ...
//	reconciler-decision:<id>:end:<region>
//
// and the digest of the lines strictly between the markers. Change a pinned
// region without updating its decision and this check refuses, naming the
// decision, the side and the region; a marker that went missing is the same
// refusal, and a marker no decision records is one too. RECONCILE_PARITY_UPDATE=1
// re-derives the digests — decisions/README.md says why a digest update is a
// change to a decision rather than a way around one.

const (
	markerPrefix     = "reconciler-decision:"
	parityRecordPath = "decisions/reconciler-parity.json"
)

// parityAnchor is one pinned region of one decision: the side whose file it
// is ("go" or "ts"), the file relative to the repository root, the region's
// name as it appears in the markers, and the digest of the lines strictly
// between them.
type parityAnchor struct {
	Side   string `json:"side"`
	File   string `json:"file"`
	Region string `json:"region"`
	Digest string `json:"digest"`
}

type parityDecision struct {
	ID       string         `json:"id"`
	Tick     string         `json:"tick"`
	Title    string         `json:"title"`
	Go       string         `json:"go"`
	Workflow string         `json:"workflow"`
	Why      string         `json:"why"`
	Anchors  []parityAnchor `json:"anchors"`
}

type parityRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Record        string           `json:"record"`
	Note          string           `json:"note"`
	Decisions     []parityDecision `json:"decisions"`
}

// loadParityRecord reads the record from the repository it is run in.
func loadParityRecord(t *testing.T, root string) parityRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, parityRecordPath))
	if err != nil {
		t.Fatalf("read %s: %v (the parity record is the tick's deliverable — it must exist and parse)", parityRecordPath, err)
	}
	var rec parityRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse %s: %v", parityRecordPath, err)
	}
	return rec
}

// marker is the comment token one region is bracketed by.
func marker(id, region, kind string) string {
	return markerPrefix + id + ":" + kind + ":" + region
}

// regionDigest pins the region's lines: one sha256 over the lines strictly
// between the markers, so any edit inside the region — and any edit that
// removes or duplicates a marker — is a mismatch or a missing marker.
func regionDigest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// extractRegion finds one marker pair and returns the lines strictly between
// them. Markers may carry prose after the token; the token itself is unique
// per decision and region, so a second occurrence of either marker is a
// malformed record rather than a second region.
func extractRegion(content, begin, end string) ([]string, error) {
	lines := strings.Split(content, "\n")
	beginAt, endAt := -1, -1
	for i, line := range lines {
		if strings.Contains(line, begin) {
			if beginAt != -1 {
				return nil, fmt.Errorf("the begin marker %s appears twice", begin)
			}
			beginAt = i
		}
		if strings.Contains(line, end) {
			if endAt != -1 {
				return nil, fmt.Errorf("the end marker %s appears twice", end)
			}
			endAt = i
		}
	}
	switch {
	case beginAt == -1:
		return nil, fmt.Errorf("no begin marker %s in the file", begin)
	case endAt == -1:
		return nil, fmt.Errorf("no end marker %s in the file", end)
	case endAt <= beginAt:
		return nil, fmt.Errorf("the end marker %s precedes its begin marker", end)
	}
	return lines[beginAt+1 : endAt], nil
}

// verifyParity checks the whole record against the tree at root: every
// anchor's region is present exactly once and digests as recorded, and no
// marker exists that no decision records. The complaints are the check's own
// words — they are what a person who tripped the check reads.
func verifyParity(root string, rec parityRecord) []string {
	var complaints []string
	seen := map[string]bool{}
	for _, d := range rec.Decisions {
		if seen[d.ID] {
			complaints = append(complaints, fmt.Sprintf("%s: the record carries decision %s twice", parityRecordPath, d.ID))
			continue
		}
		seen[d.ID] = true
		if len(d.Anchors) == 0 {
			complaints = append(complaints, fmt.Sprintf("%s: decision %s records no anchored region — a decision nothing pins cannot catch drift", parityRecordPath, d.ID))
			continue
		}
		for _, a := range d.Anchors {
			if a.Side != "go" && a.Side != "ts" {
				complaints = append(complaints, fmt.Sprintf("%s: decision %s anchors %s in %s with side %q — a side is \"go\" or \"ts\"", parityRecordPath, d.ID, a.Region, a.File, a.Side))
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.File)))
			if err != nil {
				complaints = append(complaints, fmt.Sprintf("decision %s, %s %s: %s is missing: %v", d.ID, a.Side, a.Region, a.File, err))
				continue
			}
			lines, err := extractRegion(string(data), marker(d.ID, a.Region, "begin"), marker(d.ID, a.Region, "end"))
			if err != nil {
				complaints = append(complaints, fmt.Sprintf(
					"decision %s, %s %s (%s): %v — if the behaviour moved, move the markers with it and update the decision; a recorded behaviour whose markers are gone is a change with no decision",
					d.ID, a.Side, a.Region, a.File, err))
				continue
			}
			if got := regionDigest(lines); got != a.Digest {
				complaints = append(complaints, fmt.Sprintf(
					"decision %s, %s %s (%s): the recorded region changed — digest %s, recorded %s. "+
						"If this change is the recorded behaviour moving, update the decision's prose in %s AND its digest "+
						"(re-derive with RECONCILE_PARITY_UPDATE=1) in the same change; a digest that moved without its decision "+
						"is exactly the drift this check exists to catch",
					d.ID, a.Side, a.Region, a.File, got, a.Digest, parityRecordPath))
			}
		}
	}
	complaints = append(complaints, strayMarkers(root, rec)...)
	return complaints
}

// strayMarkers scans the tree for parity markers that no decision records —
// a marker in the code whose decision was deleted (or never written) must
// not read as a pinned behaviour that verifies by silence. Test files are
// exempt: the check's own complaints name the marker shape.
func strayMarkers(root string, rec parityRecord) []string {
	known := map[string]bool{}
	for _, d := range rec.Decisions {
		for _, a := range d.Anchors {
			known[d.ID+":"+a.Region] = true
		}
	}
	var complaints []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			switch name {
			case ".git", "node_modules", "image", ".ticfac", ".tick", "runs":
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(name) {
		case ".go":
			if strings.HasSuffix(name, "_test.go") {
				return nil
			}
		case ".ts":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, ref := range markerRefs(string(data)) {
			if !known[ref] {
				id, region, _ := strings.Cut(ref, ":")
				complaints = append(complaints, fmt.Sprintf(
					"%s carries a parity marker for %s (region %s) that no decision in %s records — "+
						"a marker whose decision is gone is an unattributed behaviour, not a pin",
					rel, id, region, parityRecordPath))
			}
		}
		return nil
	})
	return complaints
}

// markerRefs reads every `reconciler-decision:<id>:<begin|end>:<region>` token
// in the content and returns the (id, region) pairs they name.
func markerRefs(content string) []string {
	var out []string
	at := 0
	for {
		idx := strings.Index(content[at:], markerPrefix)
		if idx < 0 {
			return out
		}
		start := at + idx + len(markerPrefix)
		at = start
		end := start
		for end < len(content) && isTokenChar(content[end]) {
			end++
		}
		parts := strings.Split(content[start:end], ":")
		if len(parts) != 3 || (parts[1] != "begin" && parts[1] != "end") {
			continue
		}
		out = append(out, parts[0]+":"+parts[2])
	}
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == ':':
		return true
	}
	return false
}

// TestReconcilerParityDecisionsMatchTheImplementations is the check: every
// decision in the record verifies against the tree it runs in.
//
// short: reads and hashes files only — no repository is built and no process
// is spawned, and the per-tick gate is exactly where one implementation of
// the reconciler changing a recorded behaviour without its decision must be
// caught.
//
// With RECONCILE_PARITY_UPDATE=1 it re-derives the recorded digests and
// rewrites the record when any moved — the maintenance half of the check, so
// that recording a decision's change is one command rather than a hand-hash.
// The update writes DIGESTS only: the prose is the decision, and a digest
// update without a prose update is exactly what the check's failure message
// says not to do.
func TestReconcilerParityDecisionsMatchTheImplementations(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rec := loadParityRecord(t, root)

	if os.Getenv("RECONCILE_PARITY_UPDATE") == "1" {
		updated := 0
		for i := range rec.Decisions {
			for j := range rec.Decisions[i].Anchors {
				a := &rec.Decisions[i].Anchors[j]
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.File)))
				if err != nil {
					t.Fatalf("update: read %s: %v", a.File, err)
				}
				lines, err := extractRegion(string(data), marker(rec.Decisions[i].ID, a.Region, "begin"), marker(rec.Decisions[i].ID, a.Region, "end"))
				if err != nil {
					t.Fatalf("update: decision %s, region %s: %v", rec.Decisions[i].ID, a.Region, err)
				}
				if got := regionDigest(lines); got != a.Digest {
					a.Digest = got
					updated++
				}
			}
		}
		if updated > 0 {
			data, err := json.MarshalIndent(rec, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			if err := os.WriteFile(filepath.Join(root, parityRecordPath), data, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("updated %d digest(s) in %s — the prose must move in the same change, or the record is a digest ledger", updated, parityRecordPath)
		}
	}

	for _, complaint := range verifyParity(root, rec) {
		t.Error(complaint)
	}
}

// The negative controls, the way internal/sandboxpin runs one for the
// vendored image context: a check nothing has ever seen fail is not known to
// be a check. Each control copies only the anchored files into a throwaway
// root — so a mutation cannot touch the tree the check runs against — and
// requires the verification to refuse the changed copy naming the decision,
// the side and the region.
//
// short: every control reads and hashes throwaway copies only — nothing is
// built, no process is spawned.

// parityThrowaway copies every anchored file into a temp root, preserving
// the relative paths the record names.
func parityThrowaway(t *testing.T, root string, rec parityRecord) string {
	t.Helper()
	dst := t.TempDir()
	for _, d := range rec.Decisions {
		for _, a := range d.Anchors {
			target := filepath.Join(dst, filepath.FromSlash(a.File))
			if _, err := os.Stat(target); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.File)))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dst
}

// rewrite copies src to dst with one line replaced.
func rewrite(t *testing.T, path string, edit func(line string) string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if edited := edit(line); edited != line {
			lines[i] = edited
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no line matched the edit in %s", path)
}

// short: reads and hashes a throwaway copy only — nothing is built, no
// process is spawned.
func TestParityCheckRefusesAChangedRegion(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rec := loadParityRecord(t, root)
	throwaway := parityThrowaway(t, root, rec)

	// The throwaway verifies clean first: a refusal the copy itself causes
	// says nothing about the mutation.
	if got := verifyParity(throwaway, rec); len(got) != 0 {
		t.Fatalf("the throwaway copy does not verify clean before the mutation: %v", got)
	}

	// The acceptance criterion, on one side of one decision: change exactly
	// one line of one recorded region of one implementation, and the check
	// must refuse naming the decision, the side and the region.
	const file = "cloudflare/src/epic-reconciler.ts"
	rewrite(t, filepath.Join(throwaway, filepath.FromSlash(file)), func(line string) string {
		// D26's recorded admission: the width and the row's readiness only.
		if strings.Contains(line, "if (width > 0 && live >= width) break;") {
			return strings.Replace(line, "break;", "continue;", 1)
		}
		return line
	})

	complaints := verifyParity(throwaway, rec)
	want := []string{"D26", "admission", file}
	for _, w := range want {
		found := false
		for _, c := range complaints {
			if strings.Contains(c, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the check did not name %q when a recorded region of D26 changed; complaints: %v", w, complaints)
		}
	}
}

// short: reads and hashes a throwaway copy only — nothing is built.
func TestParityCheckRefusesARemovedMarker(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rec := loadParityRecord(t, root)
	throwaway := parityThrowaway(t, root, rec)

	// A marker that goes missing is the same refusal: a recorded behaviour
	// whose bracket is deleted must not verify by silence.
	rewrite(t, filepath.Join(throwaway, "internal", "reconcile", "closeout.go"), func(line string) string {
		if strings.Contains(line, marker("D32", "prbase", "begin")) {
			return "// the begin marker was removed"
		}
		return line
	})

	complaints := verifyParity(throwaway, rec)
	found := false
	for _, c := range complaints {
		if strings.Contains(c, "no begin marker") && strings.Contains(c, "D32") {
			found = true
		}
	}
	if !found {
		t.Errorf("the check did not refuse a removed marker naming the decision; complaints: %v", complaints)
	}
}

// short: reads and hashes a throwaway copy only — nothing is built.
func TestParityCheckRefusesAMarkerNoDecisionRecords(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rec := loadParityRecord(t, root)
	throwaway := parityThrowaway(t, root, rec)

	// A marker in the code whose decision is not in the record is an
	// unattributed behaviour: deleting a decision must leave a refusal, not
	// a marker that verifies by silence.
	const file = "cloudflare/src/epic-reconciler.ts"
	rewrite(t, filepath.Join(throwaway, filepath.FromSlash(file)), func(line string) string {
		if strings.Contains(line, marker("D26", "admission", "begin")) {
			return line + "\n      // reconciler-decision:D99:begin:stray\n      // reconciler-decision:D99:end:stray"
		}
		return line
	})

	complaints := verifyParity(throwaway, rec)
	found := false
	for _, c := range complaints {
		if strings.Contains(c, "D99") && strings.Contains(c, "stray") {
			found = true
		}
	}
	if !found {
		t.Errorf("the check did not refuse a marker no decision records; complaints: %v", complaints)
	}
}

// short: reads and hashes the record only — nothing is built.
func TestParityCheckRefusesADecisionWithNoAnchors(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rec := loadParityRecord(t, root)

	// A decision nothing pins cannot catch drift, so the record itself is
	// refused: the record is not prose, it is prose each entry of which the
	// check can hold against a region.
	unpinned := rec
	unpinned.Decisions = append([]parityDecision{}, rec.Decisions...)
	unpinned.Decisions = append(unpinned.Decisions, parityDecision{ID: "D98", Title: "unpinned", Tick: "sz0"})

	complaints := verifyParity(root, unpinned)
	found := false
	for _, c := range complaints {
		if strings.Contains(c, "D98") && strings.Contains(c, "no anchored region") {
			found = true
		}
	}
	if !found {
		t.Errorf("the check did not refuse a decision with no anchors; complaints: %v", complaints)
	}
}
