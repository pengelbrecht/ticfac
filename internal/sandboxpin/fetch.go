package sandboxpin

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

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// maxArchiveEntry bounds one file read out of the tarball. The largest file in
// the image context is under 100 KiB; 8 MiB is room to grow and still refuses
// an archive that is not what it claims to be.
const maxArchiveEntry = 8 << 20

// UpstreamFile is one file as fetched from the pinned ref: its bytes, and the
// exec bit git will record for them. The bit travels with the body because the
// tree is both — a digest that could not see a flipped bit would guard the
// bytes but not the tree.
type UpstreamFile struct {
	// Body is the file's content.
	Body []byte

	// Executable reports whether the file carries an exec bit upstream.
	Executable bool
}

// Extract reads a gzipped tarball of a GitHub repository and returns every
// file under `<archive-root>/<directory>/`, keyed by base name, with its exec
// bit.
//
// Files are staged in memory and returned only when the whole archive has been
// read, so a network drop mid-fetch cannot leave a half-updated cloud/sandbox
// behind — the half-updated state this pin exists to prevent.
func Extract(r io.Reader, directory string) (map[string]UpstreamFile, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the archive is not gzip: %w", err)
	}
	defer gz.Close()

	// The archive is rooted at one directory (GitHub names it <repo>-<sha>),
	// and the tree is exactly `<root>/<directory>/`. Anchoring on the root
	// rather than searching for the segment anywhere is what keeps a Go
	// package that happens to be called cloud/sandbox from being mistaken
	// for it.
	want := strings.Trim(directory, "/") + "/"
	files := map[string]UpstreamFile{}

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
			// Nested directories are not part of the build context; the
			// flat set is.
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxArchiveEntry+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rest, err)
		}
		if len(body) > maxArchiveEntry {
			return nil, fmt.Errorf("%s is larger than %d bytes; this is not the sandbox image context", rest, maxArchiveEntry)
		}
		files[path.Base(rest)] = UpstreamFile{
			Body:       body,
			Executable: header.Mode&0o111 != 0,
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("the archive has no %s directory — the pinned ref does not carry the sandbox image context", directory)
	}
	return files, nil
}

// Diff compares the vendored tree under root against upstream, and returns one
// line per disagreement — bytes, exec bits, and the file set in both
// directions. An empty result means the vendored tree is exactly what ticks
// published at the pinned ref.
func Diff(root string, upstream map[string]UpstreamFile) ([]string, error) {
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
	for name, file := range upstream {
		if !local[name] {
			problems = append(problems, fmt.Sprintf("%s is upstream at the pinned ref and not vendored here", name))
			continue
		}
		have, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: unreadable (%v)", name, err))
			continue
		}
		haveDigest := contracts.FileDigest(have)
		wantDigest := contracts.FileDigest(file.Body)
		if haveDigest != wantDigest {
			problems = append(problems, fmt.Sprintf("%s: vendored sha256 %s, upstream %s",
				name, haveDigest, wantDigest))
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: stat (%v)", name, err))
			continue
		}
		if (info.Mode()&0o111 != 0) != file.Executable {
			problems = append(problems, fmt.Sprintf("%s: vendored mode %s, upstream %s — git carries the exec bit as part of the tree",
				name, diskMode(info), modeString(file.Executable)))
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// Write replaces the vendored tree with upstream and rewrites the pin's
// `files`, `digests` and `modes` from the bytes just written. It is called only
// by `sync`, which is never on the test path, and it leaves every other field
// of the pin exactly as it was: `ref` is a person's decision, not a side
// effect of a fetch.
func Write(root string, upstream map[string]UpstreamFile) error {
	if _, err := os.Stat(filepath.Join(root, PinFile)); err != nil {
		return fmt.Errorf("%s is unreadable: %w\n"+
			"Sync adopts a pin that already exists; it never invents one. The pin's `ref`\n"+
			"names the commit whose bytes are being adopted, and only a person chooses it.", filepath.Join(root, PinFile), err)
	}
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
	for name, file := range upstream {
		path := filepath.Join(dir, name)
		mode := os.FileMode(0o644)
		if file.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, file.Body, mode); err != nil {
			return err
		}
		// WriteFile applies the mode only to files it CREATES, so an
		// adopted mode change on an existing file is silently dropped
		// without this — a tree whose bytes move with the pin while its
		// exec bits stay behind.
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}

	return rewritePin(root, upstream)
}

// rewritePin updates `files`, `digests` and `modes` in sandbox.pin.json from
// the bytes just written, preserving the rest of the document and the key order
// the file is authored in, so a sync produces a small diff rather than a
// reshuffle.
func rewritePin(root string, upstream map[string]UpstreamFile) error {
	path := filepath.Join(root, PinFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	files := make([]string, 0, len(upstream))
	digests := map[string]string{}
	modes := map[string]string{}
	for name, file := range upstream {
		files = append(files, name)
		digests[name] = contracts.FileDigest(file.Body)
		modes[name] = modeString(file.Executable)
	}
	sort.Strings(files)

	filesJSON, err := json.Marshal(files)
	if err != nil {
		return err
	}
	digestsJSON, err := json.Marshal(digests)
	if err != nil {
		return err
	}
	modesJSON, err := json.Marshal(modes)
	if err != nil {
		return err
	}
	document["files"] = filesJSON
	document["digests"] = digestsJSON
	document["modes"] = modesJSON

	out, err := marshalPin(document)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// marshalPin writes the pin with its keys in a fixed, readable order — the
// order the file is authored in.
func marshalPin(document map[string]json.RawMessage) ([]byte, error) {
	order := []string{"$comment", "mode", "repository", "ref", "directory", "files", "digests", "modes"}
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

// modeString renders an exec bit as the git mode the pin carries for it.
func modeString(executable bool) string {
	if executable {
		return "0755"
	}
	return "0644"
}
