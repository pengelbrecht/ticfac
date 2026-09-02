package contracts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PinFile is the consumer's pin, at the repository root.
const PinFile = "contracts.pin.json"

// Pin is contracts.pin.json: how ticfac got `contracts/`, and what it may be.
//
// Its shape mirrors ticks' cloud/factory/contracts.pin.json, which is the
// design this vendoring is taken from (cloud/factory/CONTRACTS.md). Two fields
// carry different claims and move independently:
//
//   - Ref is a mechanical fact about a download: which ticks COMMIT the
//     vendored bytes came from.
//   - BundleVersion is a claim about behaviour: which contract version
//     ticfac's code was written against. SPEC §3.2 requires it by exact value.
type Pin struct {
	Comment string `json:"$comment,omitempty"`

	// BundleVersion is the exact contracts/bundle.json version this
	// repository is built against. Checked in every mode.
	BundleVersion string `json:"bundleVersion"`

	// Mode is "pinned" — the vendored copy came from a ticks ref and its
	// digests are recorded here. "workspace" (ticks' own in-tree mode) has no
	// meaning in this repository and is refused, because there is no ticks
	// checkout for it to mean.
	Mode string `json:"mode"`

	// Repository is the GitHub repository the copy came from, "owner/name".
	Repository string `json:"repository"`

	// Ref is the immutable commit sha the copy was fetched at. A branch name
	// is refused: a moving ref pins nothing.
	Ref string `json:"ref"`

	// Directory is the path inside that repository holding the bundle.
	Directory string `json:"directory"`

	// Files is the set of contracts the readers here import. Checked against
	// the bundle's file list in BOTH directions: a contract read but not
	// pinned is one nothing verifies, and a contract pinned but not read is a
	// fixture with a single reader, which contracts/README.md is explicit
	// detects nothing.
	Files []string `json:"files"`

	// Digests records the sha256 of every vendored file — the fixtures plus
	// bundle.json, CHANGELOG.md and README.md, which travel with them because
	// a copy that cannot say which bundle version it is has been copied, not
	// pinned.
	Digests map[string]string `json:"digests"`
}

// TarballURL is where `sync` fetches the pinned ref from.
//
// GitHub's codeload endpoint at an immutable commit sha, rather than ticks'
// own choice of the Go module proxy: the proxy needs a RESOLVED module version
// and ticks publishes none for this commit, while a sha is exact by
// construction and needs no resolution step that could pick different bytes.
func (p *Pin) TarballURL() string {
	return fmt.Sprintf("https://codeload.github.com/%s/tar.gz/%s", p.Repository, p.Ref)
}

// LoadPin reads and structurally validates contracts.pin.json under root.
func LoadPin(root string) (*Pin, error) {
	path := filepath.Join(root, PinFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w\n"+
			"It is the pin: without it the vendored contracts/ is an unversioned copy that\n"+
			"nothing verifies. Restore it from git rather than removing the check.", path, err)
	}
	var p Pin
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	if !versionPattern.MatchString(p.BundleVersion) {
		return nil, fmt.Errorf("%s: bundleVersion %q is not MAJOR.MINOR.PATCH", path, p.BundleVersion)
	}
	if p.Mode != "pinned" {
		return nil, fmt.Errorf("%s: mode %q — this repository has only one mode, \"pinned\".\n"+
			"There is no ticks checkout here for a workspace mode to point at.", path, p.Mode)
	}
	if !strings.Contains(p.Repository, "/") {
		return nil, fmt.Errorf("%s: repository %q is not owner/name", path, p.Repository)
	}
	if len(p.Ref) != 40 || strings.Trim(p.Ref, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("%s: ref %q is not a full 40-character commit sha.\n"+
			"A branch or a short sha is not immutable, and a pin to something that moves\n"+
			"pins nothing.", path, p.Ref)
	}
	if p.Directory == "" {
		return nil, fmt.Errorf("%s: directory is empty", path)
	}
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("%s: \"files\" is empty", path)
	}
	if !sort.StringsAreSorted(p.Files) {
		return nil, fmt.Errorf("%s: \"files\" must be sorted", path)
	}
	if len(p.Digests) == 0 {
		return nil, fmt.Errorf("%s: \"digests\" is empty — nothing is verified offline", path)
	}
	for name, digest := range p.Digests {
		if !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("%s: digests[%q] is not a lower-case sha256", path, name)
		}
	}
	return &p, nil
}

// VerifyPin is the offline gate. It makes no network call, so no network
// failure can turn a check green by skipping it.
//
// It asserts, in order:
//
//  1. the pin parses and names a known mode;
//  2. the vendored bundle verifies against its own manifest (Verify), and the
//     changelog has an entry for the version;
//  3. the bundle's version is exactly bundleVersion;
//  4. the pinned file set equals the bundle's file set, both directions;
//  5. every vendored file — fixtures, manifest, changelog, readme — hashes to
//     the digest recorded here, and no vendored file is missing or extra.
func VerifyPin(root string) error {
	p, err := LoadPin(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, DirName)

	if err := Verify(dir); err != nil {
		return err
	}
	b, err := Load(dir)
	if err != nil {
		return err
	}
	if err := VerifyChangelog(dir, b.Version); err != nil {
		return err
	}
	if b.Version != p.BundleVersion {
		return fmt.Errorf("the vendored bundle is version %s; %s pins %s.\n"+
			"Adopting a contract version is a deliberate act: move `ref` and `bundleVersion`\n"+
			"together and re-run `go run ./cmd/contracts sync`.", b.Version, PinFile, p.BundleVersion)
	}

	var problems []string

	pinned := map[string]bool{}
	for _, name := range p.Files {
		pinned[name] = true
	}
	for _, name := range b.Files {
		if !pinned[name] {
			problems = append(problems, fmt.Sprintf(
				"%s is in the bundle and not in %s: nothing here pins or verifies it", name, PinFile))
		}
	}
	for _, name := range p.Files {
		if _, ok := b.Digests[name]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is pinned in %s and not in the bundle", name, PinFile))
		}
	}

	// Every vendored file, not only the fixtures: the manifest and the
	// changelog are what let the copy say which version it is.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s is unreadable: %w", dir, err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		onDisk[e.Name()] = true
	}
	for name, want := range p.Digests {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: pinned and unreadable (%v)", name, err))
			continue
		}
		if got := FileDigest(raw); got != want {
			problems = append(problems, fmt.Sprintf(
				"%s: sha256 %s, but %s records %s — the vendored copy was edited here",
				name, got, PinFile, want))
		}
	}
	for name := range onDisk {
		if _, ok := p.Digests[name]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is in %s and not pinned in %s — an unverified file in a verified directory",
				name, dir, PinFile))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the vendored contract bundle does not match %s:\n  %s\n\n%s",
			PinFile, strings.Join(problems, "\n  "),
			"A vendored fixture is never edited in this repository. Change it in ticks, cut a\n"+
				"bundle version there, then move `ref` and `bundleVersion` here and re-run\n"+
				"`go run ./cmd/contracts sync`.")
	}
	return nil
}
