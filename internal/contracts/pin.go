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

// Pin is contracts.pin.json: how ticfac gets the two contracts that stayed
// ticks' after the ownership split (tick 4i8).
//
// ticfac authors its own bundle — contracts/bundle.json carries a ticfac
// version, ticfac's changelog travels in contracts/CHANGELOG.md, and every
// contract describing a ticfac format is edited here and re-cut here. ticks'
// bundle 7.0.0 came down to exactly two files, the published `tk --json`
// surface and the tracker's on-disk layout, and THIS pin is how those two
// stay vendored: a copy of ticks' published bytes, bound to the ticks commit
// and ticks bundle version they were fetched at.
//
// Two fields carry different claims and move independently:
//
//   - Ref is a mechanical fact about a download: which ticks COMMIT the
//     vendored bytes came from.
//   - BundleVersion is which ticks BUNDLE those bytes belong to. Offline it is
//     a recorded claim only — nothing on this side can re-derive it — which is
//     why `verify-upstream` (online, CI) fetches ticks at Ref and requires the
//     pin's digests to be exactly what ticks' own manifest publishes for that
//     version.
type Pin struct {
	Comment string `json:"$comment,omitempty"`

	// BundleVersion is the ticks bundle version the vendored bytes were
	// published at. Verified against ticks' own manifest by verify-upstream;
	// offline it is part of the pin's record, not a checkable fact.
	BundleVersion string `json:"bundleVersion"`

	// Mode is "pinned" — the vendored copy came from a ticks ref and its
	// digests are recorded here. "workspace" (ticks' own in-tree mode) has no
	// meaning in this repository and is refused, because there is no ticks
	// checkout here for it to point at.
	Mode string `json:"mode"`

	// Repository is the GitHub repository the copy came from, "owner/name".
	Repository string `json:"repository"`

	// Ref is the immutable commit sha the copy was fetched at. A branch name
	// is refused: a moving ref pins nothing.
	Ref string `json:"ref"`

	// Directory is the path inside that repository holding the bundle.
	Directory string `json:"directory"`

	// Files is the set of contracts vendored from ticks — the ticks-owned
	// half of contracts/. Checked against ticfac's own bundle in ONE
	// direction: every pinned file must ride inside the ticfac bundle too, so
	// the offline check and the readers' map cover it. The other direction is
	// the ownership statement itself: ticfac's bundle carries files ticks
	// never had, and that is the point of the split.
	Files []string `json:"files"`

	// Digests records the sha256 of every vendored file, exactly as ticks
	// published it at BundleVersion — which is what makes a local edit of a
	// ticks-owned contract detectable offline, and what verify-upstream can
	// compare against ticks' own manifest.
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
			"It is the pin: without it the two ticks-owned contracts in contracts/ are an\n"+
			"unversioned copy that nothing verifies. Restore it from git rather than\n"+
			"removing the check.", path, err)
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
	pinned := map[string]bool{}
	for _, name := range p.Files {
		if !contractNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%s: %q is not a kebab-case *.json contract name", path, name)
		}
		if _, ok := p.Digests[name]; !ok {
			return nil, fmt.Errorf("%s: %s is pinned and has no digest — nothing verifies it offline", path, name)
		}
		pinned[name] = true
	}
	for name := range p.Digests {
		if !pinned[name] {
			return nil, fmt.Errorf("%s: a digest is recorded for %s, which \"files\" does not pin", path, name)
		}
	}
	return &p, nil
}

// VerifyPin is the offline gate. It makes no network call, so no network
// failure can turn a check green by skipping it.
//
// ticfac's bundle and the ticks pin are verified together, because the
// contracts directory is ONE directory that two authorities describe:
//
//  1. the pin parses and names a known mode, and every pinned file has a
//     recorded digest;
//  2. ticfac's own bundle verifies against its manifest (Verify) — every
//     listed file present, parsing, hashing to its recorded digest, no
//     unlisted *.json sitting alongside them — and the changelog has an
//     entry for ticfac's version;
//  3. every pinned ticks file rides INSIDE ticfac's bundle, so the offline
//     check and the readers' map cover it here too;
//  4. every pinned file hashes to the digest the pin records — ticks'
//     published digest at bundleVersion, so a local edit of a ticks-owned
//     contract is caught by two independent records;
//  5. no unpinned stray: every file in contracts/ is either listed by
//     ticfac's manifest or is the manifest, the changelog or the readme
//     itself.
//
// What this cannot check offline is the pin's `bundleVersion` — the binding
// between "7.0.0" and these bytes lives in ticks' own manifest, which is not
// vendored. `verify-upstream` fetches it and closes that gap online.
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

	var problems []string

	inBundle := map[string]bool{}
	for _, name := range b.Files {
		inBundle[name] = true
	}
	pinned := map[string]bool{}
	for _, name := range p.Files {
		pinned[name] = true
		if !inBundle[name] {
			problems = append(problems, fmt.Sprintf(
				"%s is pinned in %s but not listed in %s — it rides outside the bundle, so the\n      offline check and the readers' map do not cover it here", name, PinFile, BundleFile))
		}
	}

	for _, name := range p.Files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: pinned and unreadable (%v)", name, err))
			continue
		}
		if got := FileDigest(raw); got != p.Digests[name] {
			problems = append(problems, fmt.Sprintf(
				"%s: sha256 %s, but %s records %s — ticks published these bytes, and the\n      vendored copy was edited here",
				name, got, PinFile, p.Digests[name]))
		}
	}

	// No unverified file in a verified directory: everything on disk is
	// either a contract ticfac's manifest lists, or one of the three files
	// that carry the bundle's own identity.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s is unreadable: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if inBundle[name] || name == BundleFile || name == ChangelogFile || name == ReadmeFile {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s is in %s and neither ticfac's %s lists it nor the ticks pin covers it —\n      an unverified file in a verified directory",
			name, dir, BundleFile))
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the vendored ticks contracts do not match %s:\n  %s\n\n%s",
			PinFile, strings.Join(problems, "\n  "),
			"A ticks-owned contract is never edited in this repository. Change it in ticks,\n"+
				"cut a bundle version there, then move `ref` and `bundleVersion` here and re-run\n"+
				"`go run ./cmd/contracts sync`.")
	}
	return nil
}
