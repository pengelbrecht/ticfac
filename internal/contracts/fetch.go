package contracts

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxArchiveEntry bounds one file read out of the tarball. The largest
// contract in the bundle is under 100 KiB; 8 MiB is room to grow and still
// refuses an archive that is not what it claims to be.
const maxArchiveEntry = 8 << 20

// ExtractBundle reads a gzipped tarball of a GitHub repository and returns
// every file under `<archive-root>/<directory>/`, keyed by base name.
//
// Files are staged in memory and returned only when the whole archive has been
// read, so a network drop mid-fetch cannot leave a half-updated contracts/
// behind — the half-updated state these fixtures exist to prevent.
func ExtractBundle(r io.Reader, directory string) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the archive is not gzip: %w", err)
	}
	defer gz.Close()

	// The archive is rooted at one directory (GitHub names it <repo>-<sha>),
	// and the bundle is exactly `<root>/<directory>/`. Anchoring on the root
	// rather than searching for the segment anywhere is what keeps
	// `internal/contracts/` from being mistaken for it.
	want := strings.Trim(directory, "/") + "/"
	files := map[string][]byte{}

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the archive is corrupt: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		_, inRoot, ok := strings.Cut(header.Name, "/")
		if !ok || !strings.HasPrefix(inRoot, want) {
			continue
		}
		rest := inRoot[len(want):]
		if rest == "" || strings.Contains(rest, "/") {
			// Nested directories are not part of the bundle; the flat set is.
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxArchiveEntry+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rest, err)
		}
		if len(body) > maxArchiveEntry {
			return nil, fmt.Errorf("%s is larger than %d bytes; this is not a contract bundle", rest, maxArchiveEntry)
		}
		files[path.Base(rest)] = body
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("the archive has no %s/ directory — the pinned ref does not carry the bundle", directory)
	}
	return files, nil
}

// Diff compares the vendored ticks-owned contracts under root against what
// ticks published at the pinned ref, and returns one line per disagreement.
// An empty result means the vendored bytes are exactly what ticks published,
// at the ticks bundle version the pin names.
//
// It also closes the one gap the offline check cannot: ticks' own bundle
// manifest travels with the fetch, so `bundleVersion` is bound to bytes HERE
// — the pin's recorded digests must be exactly what ticks' manifest at this
// ref publishes for that version. Offline those digests are a recorded claim;
// online they are checked against the authority that cut them.
func Diff(root string, upstream map[string][]byte) ([]string, error) {
	pin, err := LoadPin(root)
	if err != nil {
		return nil, err
	}

	var problems []string

	// ticks' manifest at the pinned ref: the authority that says which
	// bundle version these bytes are.
	manifest, ok := upstream[BundleFile]
	if !ok {
		return nil, fmt.Errorf("the fetch carries no %s — the pinned ref does not publish a bundle manifest", BundleFile)
	}
	var b Bundle
	if err := json.Unmarshal(manifest, &b); err != nil {
		return nil, fmt.Errorf("upstream %s is not valid JSON: %w", BundleFile, err)
	}
	if b.Version != pin.BundleVersion {
		problems = append(problems, fmt.Sprintf(
			"ticks at %s publishes bundle %s; %s pins %s",
			pin.Ref[:12], b.Version, PinFile, pin.BundleVersion))
	}
	if !sort.StringsAreSorted(b.Files) {
		problems = append(problems, "upstream bundle.json is not sorted")
	}
	pinned := map[string]bool{}
	for _, name := range pin.Files {
		pinned[name] = true
		if !contains(b.Files, name) {
			problems = append(problems, fmt.Sprintf(
				"%s is pinned here and ticks' bundle %s does not carry it", name, b.Version))
		}
	}
	for _, name := range b.Files {
		if !pinned[name] {
			problems = append(problems, fmt.Sprintf(
				"%s is in ticks' bundle %s and not pinned here", name, b.Version))
		}
	}
	for _, name := range pin.Files {
		published, ok := b.Digests[name]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"ticks' bundle %s records no digest for %s", b.Version, name))
			continue
		}
		if pin.Digests[name] != published {
			problems = append(problems, fmt.Sprintf(
				"%s: %s records %s, ticks' bundle %s publishes %s — the pin's bytes and its version disagree",
				name, PinFile, pin.Digests[name], b.Version, published))
		}
	}

	// The bytes on disk, against the bytes upstream.
	dir := filepath.Join(root, DirName)
	for _, name := range pin.Files {
		body, ok := upstream[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is upstream-pinned and absent from the fetch", name))
			continue
		}
		have, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: unreadable (%v)", name, err))
			continue
		}
		if FileDigest(have) != FileDigest(body) {
			problems = append(problems, fmt.Sprintf("%s: vendored sha256 %s, upstream %s",
				name, FileDigest(have), FileDigest(body)))
		}
	}

	sort.Strings(problems)
	return problems, nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// Write replaces the vendored ticks-owned contracts with upstream and
// rewrites the pin's digests. It is called only by `sync`, which is never on
// the test path — and it touches NOTHING else in contracts/: the ticfac-owned
// contracts, ticfac's manifest and ticfac's changelog are authored here, not
// fetched, and a fetch that overwrote them would be a fetch that destroyed
// work.
//
// A fetch that changes a vendored file leaves ticfac's own manifest stale on
// purpose: `sync` finishes by running the offline gate, which fails naming
// exactly that until the ticfac bundle is re-cut (bump `version`, refresh the
// digests, add the CHANGELOG entry) in the same deliberate act.
func Write(root string, upstream map[string][]byte) error {
	pin, err := LoadPin(root)
	if err != nil {
		return err
	}

	// Stage everything first: a fetch that cannot resolve every pinned file
	// writes nothing at all.
	staged := map[string][]byte{}
	for _, name := range pin.Files {
		body, ok := upstream[name]
		if !ok {
			return fmt.Errorf("the pinned ref does not carry %s — nothing was written; a partial vendor is exactly the half-updated state these fixtures exist to prevent", name)
		}
		staged[name] = body
	}

	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range staged {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
	}

	return rewritePinDigests(root, staged)
}

// rewritePinDigests updates `digests` in contracts.pin.json from the bytes
// just written, and leaves every other field — the version, the ref, the file
// list — exactly as it was: the pin's identity is a person's decision, not a
// side effect of a fetch.
func rewritePinDigests(root string, staged map[string][]byte) error {
	path := filepath.Join(root, PinFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	digests := map[string]string{}
	for name, body := range staged {
		digests[name] = FileDigest(body)
	}
	digestsJSON, err := json.Marshal(digests)
	if err != nil {
		return err
	}
	document["digests"] = digestsJSON

	out, err := marshalPin(document)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// marshalPin writes the pin with its keys in a fixed, readable order — the
// order the file is authored in — so a sync produces a small diff rather than
// a reshuffle.
func marshalPin(document map[string]json.RawMessage) ([]byte, error) {
	order := []string{"$comment", "bundleVersion", "mode", "repository", "ref", "directory", "files", "digests"}
	seen := map[string]bool{}
	var out strings.Builder
	out.WriteString("{\n")
	first := true
	emit := func(key string) error {
		value, ok := document[key]
		if !ok {
			return nil
		}
		seen[key] = true
		if !first {
			out.WriteString(",\n")
		}
		first = false
		var pretty strings.Builder
		if err := indentInto(&pretty, value); err != nil {
			return err
		}
		fmt.Fprintf(&out, "  %q: %s", key, pretty.String())
		return nil
	}
	for _, key := range order {
		if err := emit(key); err != nil {
			return nil, err
		}
	}
	rest := make([]string, 0, len(document))
	for key := range document {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		if err := emit(key); err != nil {
			return nil, err
		}
	}
	out.WriteString("\n}\n")
	return []byte(out.String()), nil
}

func indentInto(dst *strings.Builder, value json.RawMessage) error {
	var buf strings.Builder
	if err := jsonIndent(&buf, value); err != nil {
		return err
	}
	// Re-indent the nested block by two spaces so it sits under its key.
	lines := strings.Split(buf.String(), "\n")
	for i, line := range lines {
		if i > 0 {
			dst.WriteString("\n  ")
		}
		dst.WriteString(line)
	}
	return nil
}

func jsonIndent(dst *strings.Builder, value json.RawMessage) error {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	// Sorted keys, two-space indent: encoding/json's map marshalling is
	// already sorted, which is what keeps a re-sync byte-stable.
	out, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return err
	}
	dst.Write(out)
	return nil
}
