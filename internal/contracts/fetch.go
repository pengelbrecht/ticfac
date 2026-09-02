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
// contract in bundle 3.0.0 is under 100 KiB; 8 MiB is room to grow and still
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

// Diff compares the vendored copy under root against upstream, and returns one
// line per disagreement. An empty result means the vendored bytes are exactly
// what ticks published at the pinned ref.
func Diff(root string, upstream map[string][]byte) ([]string, error) {
	dir := filepath.Join(root, DirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w", dir, err)
	}

	var problems []string
	local := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		local[e.Name()] = true
		if _, ok := upstream[e.Name()]; !ok {
			problems = append(problems, fmt.Sprintf("%s is vendored here and absent upstream at the pinned ref", e.Name()))
		}
	}
	for name, body := range upstream {
		if !local[name] {
			problems = append(problems, fmt.Sprintf("%s is upstream at the pinned ref and not vendored here", name))
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

// Write replaces the vendored bundle with upstream and rewrites the pin's
// digests. It is called only by `sync`, which is never on the test path.
func Write(root string, upstream map[string][]byte) error {
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s is unreadable: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := upstream[e.Name()]; !ok {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	for name, body := range upstream {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
	}

	return rewritePinDigests(root, upstream)
}

// rewritePinDigests updates `files` and `digests` in contracts.pin.json from
// the bytes just written, and leaves every other field exactly as it was: the
// pin's version and ref are a person's decision, not a side effect of a fetch.
func rewritePinDigests(root string, upstream map[string][]byte) error {
	path := filepath.Join(root, PinFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	b, err := Load(filepath.Join(root, DirName))
	if err != nil {
		return err
	}
	filesJSON, err := json.Marshal(b.Files)
	if err != nil {
		return err
	}
	document["files"] = filesJSON

	digests := map[string]string{}
	for name, body := range upstream {
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
