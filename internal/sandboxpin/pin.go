// Package sandboxpin is the drift guard for cloud/sandbox.
//
// cloud/sandbox — the sandbox image's build context, the container a cloud run
// boots in either role — is owned by ticks. ticks' internal/sandbox suite runs
// those scripts; this repository carries the same tree as the factory's
// embedded image context (embedded.go's sandboxFS). Two repositories carrying
// one tree with nothing between them is how the extraction epic's premise
// reads in reverse: "extracted" cannot mean "copied". If the copies drift,
// ticfac's cold-start budget bounds a container built from THIS copy while
// ticks' tests guard ITS copy, and both keep passing while the thing that ships
// diverges from the thing that is tested.
//
// So the tree is vendored and pinned exactly the way the contract bundle is
// (CONTRACTS.md; the design is ticks' cloud/factory/CONTRACTS.md applied
// twice): ticks authors it, this repository consumes it at an immutable
// commit, sandbox.pin.json records the bytes and the git modes, and the check
// is re-implemented here rather than imported — the standing rule that ticfac
// never imports a ticks Go package.
//
// Three commands, and the split between them is the whole safety argument:
//
//	check            every test run, every CI run. NEVER touches the network.
//	verify-upstream  CI only. Fetches the pinned ref and compares.
//	sync             a person adopting a new pin. Requires the network.
//
// `check` is the gate and makes no network call, so no network failure can
// turn a test run green by skipping it. `sync` is the only thing that writes,
// and it is not on the test path.
//
// No path through this package warns and continues.
package sandboxpin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

const (
	// PinFile is the consumer's pin, at the repository root.
	PinFile = "sandbox.pin.json"

	// DirName is where the vendored image context lives, relative to the
	// repository root: exactly where ticks' own copy sits, so a reader ported
	// from ticks resolves the same relative path.
	DirName = "cloud/sandbox"
)

// Pin is sandbox.pin.json: how ticfac got cloud/sandbox, and what it may be.
//
// Its shape mirrors contracts.pin.json, with one addition this tree needs:
// `modes`. Git carries the exec bit as part of the tree, and a tree digest
// that could not see a flipped bit would guard the bytes but not the tree.
type Pin struct {
	// Comment is the file's own documentation, read by people and ignored by
	// the check.
	Comment string `json:"$comment,omitempty"`

	// Mode is "pinned" — the vendored copy came from a ticks ref and its
	// digests are recorded here. "workspace" (a consumer with a ticks
	// checkout to point at) has no meaning in this repository and is
	// refused.
	Mode string `json:"mode"`

	// Repository is the GitHub repository the copy came from, "owner/name".
	Repository string `json:"repository"`

	// Ref is the immutable ticks commit whose cloud/sandbox tree the vendored
	// bytes are. A branch name is refused: a pin to something that moves pins
	// nothing.
	Ref string `json:"ref"`

	// Directory is the path inside that repository holding the tree.
	Directory string `json:"directory"`

	// Files is every vendored file, sorted, so that a diff of a pin bump is
	// readable.
	Files []string `json:"files"`

	// Digests records the sha256 of every vendored file.
	Digests map[string]string `json:"digests"`

	// Modes records the git mode of every vendored file: "0644" or "0755",
	// the only two git can carry.
	Modes map[string]string `json:"modes"`
}

// TarballURL is where `sync` fetches the pinned ref from: GitHub's codeload
// endpoint at an immutable commit sha, exact by construction and needing no
// resolution step that could pick different bytes.
func (p *Pin) TarballURL() string {
	return fmt.Sprintf("https://codeload.github.com/%s/tar.gz/%s", p.Repository, p.Ref)
}

// LoadPin reads and structurally validates sandbox.pin.json under root.
func LoadPin(root string) (*Pin, error) {
	path := filepath.Join(root, PinFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w\n"+
			"It is the pin: without it the vendored cloud/sandbox is an unversioned copy that\n"+
			"nothing verifies. Restore it from git rather than removing the check.", path, err)
	}
	var p Pin
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	if p.Mode != "pinned" {
		return nil, fmt.Errorf("%s: mode %q — this repository has only one mode, \"pinned\".\n"+
			"There is no ticks checkout here for a workspace mode to point at.", path, p.Mode)
	}
	if !strings.Contains(p.Repository, "/") || strings.Contains(p.Repository, "..") {
		return nil, fmt.Errorf("%s: repository %q is not owner/name", path, p.Repository)
	}
	if !isFullSHA(p.Ref) {
		return nil, fmt.Errorf("%s: ref %q is not a full 40-character commit sha.\n"+
			"A branch or a short sha is not immutable, and a pin to something that moves\n"+
			"pins nothing.", path, p.Ref)
	}
	if p.Directory == "" || filepath.IsAbs(p.Directory) || strings.Contains(p.Directory, "..") {
		return nil, fmt.Errorf("%s: directory %q is not a path inside the repository", path, p.Directory)
	}
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("%s: \"files\" is empty", path)
	}
	if !sort.StringsAreSorted(p.Files) {
		return nil, fmt.Errorf("%s: \"files\" must be sorted so that a diff of a pin bump is readable", path)
	}
	if len(p.Digests) == 0 {
		return nil, fmt.Errorf("%s: \"digests\" is empty — nothing is verified offline", path)
	}

	// The three sets must agree in every direction: a file pinned but not
	// digested is verified for nothing, a digest with no mode is a tree half
	// described, and an entry in one map missing from the others is a file
	// the check silently skips.
	seen := map[string]bool{}
	for _, name := range p.Files {
		if seen[name] {
			return nil, fmt.Errorf("%s: %q is listed twice in \"files\"", path, name)
		}
		seen[name] = true
		if !isPlainFileName(name) {
			return nil, fmt.Errorf("%s: %q is not a plain file name in the tree", path, name)
		}
		digest, ok := p.Digests[name]
		if !ok {
			return nil, fmt.Errorf("%s: %s is listed but carries no digest — nothing verifies it", path, name)
		}
		if !isSHA256(digest) {
			return nil, fmt.Errorf("%s: digests[%q] is not a lower-case sha256", path, name)
		}
		mode, ok := p.Modes[name]
		if !ok {
			return nil, fmt.Errorf("%s: %s is listed but carries no mode — the tree is bytes AND the git mode, and a digest that cannot see the exec bit guards only half of it", path, name)
		}
		if mode != "0644" && mode != "0755" {
			return nil, fmt.Errorf("%s: modes[%q] is %q, not \"0644\" or \"0755\" — git records no other file mode", path, name, mode)
		}
	}
	for name := range p.Digests {
		if !seen[name] {
			return nil, fmt.Errorf("%s: digests[%q] is recorded for a file the pin does not list", path, name)
		}
	}
	for name := range p.Modes {
		if !seen[name] {
			return nil, fmt.Errorf("%s: modes[%q] is recorded for a file the pin does not list", path, name)
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
//  2. every pinned file hashes to the digest recorded in the pin;
//  3. every pinned file's exec bit matches the mode recorded in the pin;
//  4. every file in cloud/sandbox is pinned — no unverified file in a
//     verified directory, in either direction.
func VerifyPin(root string) error {
	p, err := LoadPin(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, DirName)

	var problems []string

	onDisk := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s is unreadable: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			// The build context is flat; a directory in it is a vendoring
			// accident this check should name rather than walk past.
			problems = append(problems, fmt.Sprintf(
				"%s is a directory inside %s — the build context is flat, and a nested tree is not what the Dockerfile builds", e.Name(), dir))
			continue
		}
		onDisk[e.Name()] = true
	}

	for _, name := range p.Files {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"%s: pinned and unreadable (%v)", name, err))
			continue
		}
		if got := contracts.FileDigest(raw); got != p.Digests[name] {
			problems = append(problems, fmt.Sprintf(
				"%s: sha256 %s, but %s records %s — the vendored copy was edited here",
				name, got, PinFile, p.Digests[name]))
		}
		info, err := os.Stat(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: stat: %v", name, err))
			continue
		}
		if mode := diskMode(info); mode != p.Modes[name] {
			problems = append(problems, fmt.Sprintf(
				"%s: mode %s on disk, but %s records %s — git carries the exec bit as part of the tree",
				name, mode, PinFile, p.Modes[name]))
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
		return fmt.Errorf("the vendored sandbox image context does not match %s:\n  %s\n\n%s",
			PinFile, strings.Join(problems, "\n  "),
			"A vendored tree file is never edited in this repository. Change it in ticks,\n"+
				"move `ref` in "+PinFile+" to the commit that changed it, and run\n"+
				"`go run ./cmd/sandbox sync` — then commit cloud/sandbox and "+PinFile+" together.")
	}
	return nil
}

// diskMode renders a file's mode as git does: the exec bit is the only thing
// git records, so the answer is one of the two modes the pin may carry.
func diskMode(info os.FileInfo) string {
	if info.Mode()&0o111 != 0 {
		return "0755"
	}
	return "0644"
}

// isFullSHA reports whether ref is a full 40-character lower-case hex commit
// sha. A branch name or an abbreviated commit is not immutable, and a pin to
// something that moves pins nothing.
func isFullSHA(ref string) bool {
	return len(ref) == 40 && strings.Trim(ref, "0123456789abcdef") == ""
}

// isSHA256 reports whether s is a lower-case hex sha256.
func isSHA256(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// isPlainFileName reports whether name is one flat file name: the build
// context is flat, so a separator in a name is a vendoring mistake the pin
// should refuse to record rather than faithfully verify.
func isPlainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}
