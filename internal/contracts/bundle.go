// Package contracts reads the versioned cross-language contract bundle that
// ticfac vendors from ticks into the repository root `contracts/` directory.
//
// The fixtures themselves are described by contracts/README.md: behavioural
// case tables that more than one implementation asserts against, so a rule
// changed on one side and not the other fails a build instead of shipping.
// This package does not read any of them — internal/contracts/parity does.
// This package reads the BUNDLE MANIFEST that wraps them, contracts/bundle.json,
// and answers the question the individual readers cannot:
//
//	does "contract bundle version X" name a fixed set of bytes?
//
// ticfac is the consumer ticks' cloud/factory/CONTRACTS.md was designed for: a
// repository outside ticks that pins a bundle version by exact value. So the
// check is re-implemented here in ticfac's own code rather than imported —
// SPEC §3.2, and the standing rule that ticfac never imports a ticks Go
// package. Two failures, not one:
//
//   - edit a vendored fixture, and its recorded per-file digest no longer
//     matches the bytes on disk;
//   - re-cut the manifest WITHOUT bumping the version, and the per-file
//     digests agree again — which is why the manifest carries
//     `version_digests`, an append-only ledger binding each version to a
//     digest OVER its digests. A re-cut at an unchanged version contradicts
//     the ledger entry, and Verify refuses it.
//
// The second is the one a pinned consumer cannot otherwise see, and it is the
// only reason the version exists.
//
// No path through this package warns and continues.
package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// BundleFile is the manifest, relative to the contracts directory.
	BundleFile = "bundle.json"

	// ChangelogFile records what changed in each bundle version.
	ChangelogFile = "CHANGELOG.md"
)

// semver, without pre-release or build metadata.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// contractNamePattern matches the kebab-case *.json naming contracts/README.md
// prescribes for a fixture.
var contractNamePattern = regexp.MustCompile(`^[a-z0-9-]+\.json$`)

// digestPattern matches a lower-case hex sha256.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Bundle is contracts/bundle.json.
type Bundle struct {
	// Version is the pinnable identity of this exact set of fixture bytes.
	Version string `json:"version"`

	// Files is every contract in the bundle, sorted, excluding the manifest.
	Files []string `json:"files"`

	// Digests maps each entry of Files to its sha256, lower-case hex.
	Digests map[string]string `json:"digests"`

	// VersionDigests is the append-only ledger: every version this bundle has
	// been cut at, mapped to the ContentDigest of that cut.
	VersionDigests map[string]string `json:"version_digests"`
}

// ContentDigest is the sha256 of what a bundle version claims to name: the
// version string and every file's digest, in the canonical line form ticks'
// generator and cloud/factory/scripts/contracts.mjs both reproduce.
//
//	<version>\n
//	<file> <sha256>\n   (one per file, sorted by name)
//
// Three independent spellings of one convention that happen to agree is the
// check; a convention with one implementation is not one.
func (b *Bundle) ContentDigest() string {
	names := append([]string(nil), b.Files...)
	sort.Strings(names)

	var canonical strings.Builder
	canonical.WriteString(b.Version)
	canonical.WriteString("\n")
	for _, name := range names {
		canonical.WriteString(name)
		canonical.WriteString(" ")
		canonical.WriteString(b.Digests[name])
		canonical.WriteString("\n")
	}

	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

// FileDigest is the sha256 of one file's bytes, lower-case hex.
func FileDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Load reads and structurally validates the manifest in dir. It does not touch
// the fixtures; use Verify for that.
func Load(dir string) (*Bundle, error) {
	path := filepath.Join(dir, BundleFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w\n"+
			"It is the contract bundle manifest — the file that gives the vendored fixtures a\n"+
			"version ticfac can pin. Restore it from git, or re-vendor it with\n"+
			"`go run ./cmd/contracts sync`, rather than removing the check.", path, err)
	}

	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	if !versionPattern.MatchString(b.Version) {
		return nil, fmt.Errorf("%s: version %q is not MAJOR.MINOR.PATCH", path, b.Version)
	}
	if len(b.Files) == 0 {
		return nil, fmt.Errorf("%s: \"files\" is empty — a bundle with no contracts pins nothing", path)
	}

	seen := map[string]bool{}
	for _, name := range b.Files {
		switch {
		case !contractNamePattern.MatchString(name):
			return nil, fmt.Errorf("%s: %q is not a kebab-case *.json contract name", path, name)
		case name == BundleFile:
			return nil, fmt.Errorf("%s: the manifest must not list itself", path)
		case seen[name]:
			return nil, fmt.Errorf("%s: %q is listed twice", path, name)
		}
		seen[name] = true
	}
	if !sort.StringsAreSorted(b.Files) {
		return nil, fmt.Errorf("%s: \"files\" must be sorted so that a diff of a bundle bump is readable", path)
	}

	if len(b.VersionDigests) == 0 {
		return nil, fmt.Errorf("%s: \"version_digests\" is empty — without the ledger a re-cut of the\n"+
			"same version is invisible, which is the one drift a pinned consumer cannot see.", path)
	}
	for version, digest := range b.VersionDigests {
		if !versionPattern.MatchString(version) {
			return nil, fmt.Errorf("%s: version_digests key %q is not MAJOR.MINOR.PATCH", path, version)
		}
		if !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("%s: version_digests[%q] is not a lower-case sha256", path, version)
		}
	}

	return &b, nil
}

// Verify loads the manifest and asserts that the contracts directory matches
// it exactly: every listed file present, parsing, and hashing to the recorded
// digest, and no unlisted contract sitting alongside them.
func Verify(dir string) error {
	b, err := Load(dir)
	if err != nil {
		return err
	}

	var problems []string

	listed := map[string]bool{}
	for _, name := range b.Files {
		listed[name] = true
	}

	onDisk, err := FilesOnDisk(dir)
	if err != nil {
		return err
	}
	for _, name := range onDisk {
		if !listed[name] {
			problems = append(problems, fmt.Sprintf(
				"%s: present in %s but not listed in %s — it is unversioned, so nothing "+
					"vendors or verifies it", name, dir, BundleFile))
		}
	}

	for _, name := range b.Files {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: listed in %s but unreadable (%v)", name, BundleFile, err))
			continue
		}
		if !json.Valid(raw) {
			problems = append(problems, fmt.Sprintf("%s: not valid JSON", name))
			continue
		}
		expected, ok := b.Digests[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no sha256 recorded in %s", name, BundleFile))
			continue
		}
		if actual := FileDigest(raw); actual != expected {
			problems = append(problems, fmt.Sprintf(
				"%s: sha256 %s\n      bundle %s says %s", name, actual, b.Version, expected))
		}
	}

	for name := range b.Digests {
		if !listed[name] {
			problems = append(problems, fmt.Sprintf("%s: a digest is recorded for a file %s does not list", name, BundleFile))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("contract bundle %s does not match %s:\n  %s\n\n%s",
			b.Version, dir, strings.Join(problems, "\n  "),
			"The vendored bundle is not the bundle it says it is. ticfac pins a bundle\n"+
				"version by exact value, so the same version string must never mean two\n"+
				"different sets of bytes. A vendored fixture is NOT edited here: change it in\n"+
				"ticks, cut a new bundle version there, then move `ref` and `bundleVersion` in\n"+
				"contracts.pin.json and run `go run ./cmd/contracts sync`.")
	}

	// The manifest agrees with the fixtures. The remaining question is the one
	// the per-file digests cannot answer, because a re-cut makes them agree
	// again: does THIS version still name the bytes it named when it was cut?
	recorded, ok := b.VersionDigests[b.Version]
	if !ok {
		return fmt.Errorf("%s: version %s has no entry in \"version_digests\".\n"+
			"Every cut version records the digest of its own digests map, so that re-cutting\n"+
			"one at an unchanged version is visible.", filepath.Join(dir, BundleFile), b.Version)
	}
	if actual := b.ContentDigest(); actual != recorded {
		return fmt.Errorf("contract bundle %s was cut before with different bytes:\n"+
			"  digest of this cut %s\n"+
			"  version_digests[%s] %s\n\n%s",
			b.Version, actual, b.Version, recorded,
			"A fixture changed and the manifest was re-cut WITHOUT bumping \"version\". The\n"+
				"per-file digests agree again, so nothing else can see it — and a consumer\n"+
				"pinned to this version by exact value cannot see it at all, which is the whole\n"+
				"reason the version exists.")
	}

	return nil
}

// VerifyChangelog asserts that contracts/CHANGELOG.md has an entry for
// version. A version with no entry says nothing about what adopting it costs,
// which is the only reason a consuming repository pins one.
func VerifyChangelog(dir, version string) error {
	path := filepath.Join(dir, ChangelogFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s is unreadable: %w\n"+
			"The changelog travels with the vendored fixtures: a copy that cannot say what\n"+
			"its version changed has been copied, not pinned.", path, err)
	}

	heading := "## " + version
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == heading {
			return nil
		}
	}

	return fmt.Errorf("%s has no `%s` entry for contract bundle version %s.", path, heading, version)
}

// SchemaIDUse is one appearance of a `schema_id` in a contract file: either the
// DEFINITION of that record (the object carrying the id also carries its
// `schema`) or a REFERENCE to it (the object names the id and nothing else).
type SchemaIDUse struct {
	// File is the contract the appearance is in.
	File string

	// Pointer is a readable path to the object, e.g. `records.evidence`.
	Pointer string

	// Defines is true when this appearance carries the schema itself.
	Defines bool
}

// VerifySchemaIDs asserts the bundle-wide rule bundle 2.0.0 added: a schema_id
// that appears in more than one contract file resolves to exactly one
// definition.
//
// Bundle 1.2.0 described one file — .ticfac/runs/<run-id>/evidence/<key>.json —
// twice, and no document satisfied both schemas, because nothing in the bundle
// was looking ACROSS files. This looks across. An id confined to a single file
// is that file's business: the rule is about the seam.
func VerifySchemaIDs(dir string) error {
	uses, err := SchemaIDUses(dir)
	if err != nil {
		return err
	}

	var problems []string
	for id, appearances := range uses {
		files := map[string]bool{}
		var definitions []SchemaIDUse
		for _, use := range appearances {
			files[use.File] = true
			if use.Defines {
				definitions = append(definitions, use)
			}
		}
		if len(files) < 2 {
			continue
		}

		switch len(definitions) {
		case 1:
			continue
		case 0:
			problems = append(problems, fmt.Sprintf(
				"%s: referenced by %s and defined nowhere — a pointer at nothing",
				id, strings.Join(sortedKeys(files), ", ")))
		default:
			var where []string
			for _, def := range definitions {
				where = append(where, fmt.Sprintf("%s %s", def.File, def.Pointer))
			}
			sort.Strings(where)
			problems = append(problems, fmt.Sprintf(
				"%s is defined %d times: %s", id, len(definitions), strings.Join(where, ", ")))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the contract bundle in %s has schema ids that do not resolve to one definition:\n  %s\n\n%s",
			dir, strings.Join(problems, "\n  "),
			"One record, one schema. Two contracts describing the same record is the drift\n"+
				"this bundle exists to catch and cannot catch from inside either file.")
	}

	return nil
}

// SchemaIDUses walks every contract in dir and records each appearance of a
// `schema_id`. Exported because the parity readers assert the evidence record
// specifically, not merely that the general rule holds.
func SchemaIDUses(dir string) (map[string][]SchemaIDUse, error) {
	names, err := FilesOnDisk(dir)
	if err != nil {
		return nil, err
	}

	uses := map[string][]SchemaIDUse{}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", name, err)
		}
		collectSchemaIDs(document, name, "", uses)
	}
	return uses, nil
}

func collectSchemaIDs(node any, file, pointer string, into map[string][]SchemaIDUse) {
	switch v := node.(type) {
	case map[string]any:
		if id, ok := v["schema_id"].(string); ok && id != "" {
			_, defines := v["schema"]
			into[id] = append(into[id], SchemaIDUse{File: file, Pointer: pointer, Defines: defines})
		}
		for _, key := range sortedMapKeys(v) {
			collectSchemaIDs(v[key], file, joinPointer(pointer, key), into)
		}
	case []any:
		for i, item := range v {
			collectSchemaIDs(item, file, fmt.Sprintf("%s[%d]", pointer, i), into)
		}
	}
}

// FilesOnDisk lists the contract fixtures in dir: every *.json except the
// manifest itself.
func FilesOnDisk(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == BundleFile || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func joinPointer(pointer, key string) string {
	if pointer == "" {
		return key
	}
	return pointer + "." + key
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
