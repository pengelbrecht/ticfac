package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The module's toolchain floor, checked where a Go toolchain is pinned (tick 152).
//
// go.mod's `go` directive is a floor, not a fact about any one machine: pinning
// fang (tick nwj) dragged it from 1.24.0 up to 1.24.2, and from that moment
// every toolchain older than 1.24.2 fails to build the module — not only the
// one that builds ticfac, but any environment that pins a Go version by hand
// and then builds, vets, tests or `go install`s this module. Two environments
// here pin a Go toolchain, and each needs the floor checked differently:
//
//   - The orchestrator image carries `ARG GO_VERSION` in its Dockerfile, and
//     image/README.md already promises "bump `GO_VERSION` with `go.mod`" — a
//     promise nothing enforced. `GOTOOLCHAIN=local` in the image means the
//     broken build surfaces at deploy time, on a machine that is not this
//     one, as a compiler error naming neither the tick nor the drift.
//   - The CI workflows pin nothing by hand — every actions/setup-go reads
//     `go-version-file: go.mod`, which follows the floor by construction —
//     but nothing notices if a workflow starts pinning an explicit version,
//     and a pin below the floor is a CI that cannot build the very commit it
//     was asked to check.
//
// The guard reads the floor from go.mod itself, so raising the directive
// re-checks every pin without anyone remembering this file exists.

// moduleGoFloor reads the go directive — the minimum toolchain the module can
// be built with — out of a go.mod body. A `toolchain` line, if one ever
// appears, names a toolchain at or above the directive and is not the floor.
func moduleGoFloor(body string) (string, error) {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "go ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				return fields[1], nil
			}
		}
	}
	return "", errors.New("go.mod carries no go directive: the module's toolchain floor cannot be read")
}

// imageGoPin reads the orchestrator image's pinned Go toolchain out of a
// Dockerfile body. The image pins by hand by design — checksummed tarballs, a
// version the README's bump-with-go.mod promise hangs off — so a Dockerfile
// with no pin is a shape change to refuse, not a tree with nothing to check.
func imageGoPin(body string) (string, error) {
	m := regexp.MustCompile(`(?m)^ARG GO_VERSION=(\S+)\s*$`).FindStringSubmatch(body)
	if m == nil {
		return "", errors.New("no ARG GO_VERSION pin in the image's Dockerfile")
	}
	return m[1], nil
}

// goVersionMeets reports whether version satisfies floor, comparing the
// dotted fields as the integers they name. The lexical order is wrong exactly
// where it matters here — as strings, "1.24.11" < "1.24.2" — and a pin that
// drifts below the floor by one patch release is precisely the case this
// guard exists to catch, so it must never be judged by string comparison.
func goVersionMeets(version, floor string) error {
	v, err := goVersionNumbers(version)
	if err != nil {
		return fmt.Errorf("%q is not a plain dotted go version, so it cannot be checked against the floor", version)
	}
	f, err := goVersionNumbers(floor)
	if err != nil {
		return fmt.Errorf("the floor %q is not a plain dotted go version", floor)
	}
	for i := 0; i < len(v) || i < len(f); i++ {
		pin, floorField := 0, 0
		if i < len(v) {
			pin = v[i]
		}
		if i < len(f) {
			floorField = f[i]
		}
		switch {
		case pin < floorField:
			return fmt.Errorf("go %s is below the module's floor %s", version, floor)
		case pin > floorField:
			return nil
		}
	}
	return nil
}

func goVersionNumbers(version string) ([]int, error) {
	parts := strings.Split(version, ".")
	numbers := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a plain dotted go version", version)
		}
		numbers[i] = n
	}
	return numbers, nil
}

// explicitGoVersionPin matches a workflow's explicit `go-version:` value.
// `go-version-file:` does not match: its dash is not the colon the pattern
// requires, and following go.mod is the checked-by-construction form.
var explicitGoVersionPin = regexp.MustCompile(`^[ \t]*go-version:[ \t]*(.*)$`)

// checkWorkflowGoPins checks every explicitly pinned go-version in the
// workflow files under root against the floor. A tree with no workflows pins
// nothing by hand; a workflow following go.mod (go-version-file) has nothing
// to check. A workflow pinning anything that is not a plain version — an
// expression, "stable", a bare variable — is refused rather than skipped:
// the tick asks that explicit pins be CHECKED against the floor, and a value
// this guard cannot read is one it cannot check.
func checkWorkflowGoPins(root, floor string) error {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var violations []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			m := explicitGoVersionPin.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			value := strings.TrimSpace(m[1])
			if c := strings.Index(value, "#"); c >= 0 {
				value = strings.TrimSpace(value[:c])
			}
			value = strings.Trim(value, `"'`)
			if value == "" {
				violations = append(violations, fmt.Errorf("%s:%d pins a go-version with no value", name, i+1))
				continue
			}
			if err := goVersionMeets(value, floor); err != nil {
				violations = append(violations, fmt.Errorf("%s:%d pins %s: an explicit CI pin below the module's toolchain floor is a CI that cannot build the commit it was asked to check", name, i+1, err))
			}
		}
	}
	return errors.Join(violations...)
}

// checkGoToolchainFloor checks every Go toolchain this repository pins by hand
// against the floor go.mod names. The floor is read, never spelled here: the
// guard's whole point is that raising the go directive re-checks the pins
// without anyone remembering this file exists.
func checkGoToolchainFloor(root string) error {
	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return fmt.Errorf("reading go.mod: %w", err)
	}
	floor, err := moduleGoFloor(string(body))
	if err != nil {
		return err
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "image", sandboxDockerfileName))
	if err != nil {
		return fmt.Errorf("reading the orchestrator image's Dockerfile: %w", err)
	}
	pin, err := imageGoPin(string(dockerfile))
	if err != nil {
		return err
	}
	if err := goVersionMeets(pin, floor); err != nil {
		return fmt.Errorf("%w — image/README.md promises to bump ARG GO_VERSION with go.mod", err)
	}

	return checkWorkflowGoPins(root, floor)
}

// short: parses versions, nothing else.
//
// The comparison must be numeric field by field: "1.24.11" is ABOVE "1.24.2"
// numerically and BELOW it lexically, and the one case the guard exists to
// catch is exactly a small patch-level drift past the floor.
func TestGoVersionMeetsComparesNumbersNotStrings(t *testing.T) {
	for _, tc := range []struct {
		version, floor string
		want           string // "" for meets; a fragment of the refusal otherwise
	}{
		{"1.24.11", "1.24.2", ""},      // the image today: lexically below, numerically above
		{"1.24.2", "1.24.2", ""},       // the floor itself
		{"1.24.3", "1.24.2", ""},       // one patch above
		{"1.25.0", "1.24.2", ""},       // minor above
		{"1.25", "1.24.2", ""},         // shorter pins still compare field by field
		{"2.0", "1.24.2", ""},          // major above
		{"1.24.1", "1.24.2", "below"},  // one patch below the floor
		{"1.23.11", "1.24.2", "below"}, // a patch run cannot outrun a minor floor
		{"1.24", "1.24.2", "below"},    // a missing field is zero, not "nothing to compare"
		{"1.24.2.0", "1.24.2", ""},     // extra fields compare as zero
		{"stable", "1.24.2", "not a plain dotted go version"},
		{"${{ env.GO }}", "1.24.2", "not a plain dotted go version"},
	} {
		err := goVersionMeets(tc.version, tc.floor)
		if tc.want == "" {
			if err != nil {
				t.Errorf("go %s should meet floor %s: %v", tc.version, tc.floor, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("go %s meets floor %s, want a refusal containing %q", tc.version, tc.floor, tc.want)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("go %s vs floor %s refused with %q, want it to say %q", tc.version, tc.floor, err.Error(), tc.want)
		}
	}
}

// short: reads this tree's own go.mod, image and workflows.
//
// THE guard: every Go toolchain this repository pins by hand is checked
// against the floor go.mod names — the image's ARG GO_VERSION (1.24.11 today,
// above the 1.24.2 floor fang dragged in) and any explicit go-version a
// workflow ever starts pinning. Raising the go directive past a pin fails
// here first, in the gate, instead of at a deploy nobody is watching.
func TestTheRepoMeetsItsOwnGoToolchainFloor(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	if err := checkGoToolchainFloor(root); err != nil {
		t.Errorf("the tree pins a Go toolchain below the module's floor: %v", err)
	}
}

// short: one fixture tree per case.
//
// The negative control that proves the guard bites: the real vendored image
// with its GO_VERSION pin dropped below the real go.mod floor — exactly the
// drift image/README.md warns about — must be refused, naming the floor so
// the failure explains itself.
func TestDroppingTheImagesGoPinBelowTheFloorIsRefused(t *testing.T) {
	root := t.TempDir()
	real := realGoFloorFixture(t, root)

	lowered := regexp.MustCompile(`(?m)^ARG GO_VERSION=.*$`).ReplaceAllString(real.dockerfile, "ARG GO_VERSION=1.23.0")
	if lowered == real.dockerfile {
		t.Fatalf("could not lower the image's GO_VERSION pin:\n%s", real.dockerfile)
	}
	writeFloorFixture(t, root, real.gomod, lowered)

	err := checkGoToolchainFloor(root)
	if err == nil {
		t.Fatal("an image pinned to go 1.23.0 passed a 1.24.2 floor")
	}
	if !strings.Contains(err.Error(), "below the module's floor") {
		t.Errorf("the refusal does not name the floor it violated: %v", err)
	}
}

// short: one fixture tree.
//
// The image pins its Go by hand by design; a Dockerfile that lost the pin
// altogether is a shape change the guard must notice, not a tree with
// nothing to check — the pin's checksums, and the README's
// bump-with-go.mod promise, all hang off ARG GO_VERSION existing.
func TestAnImageWithNoGoPinAtAllIsRefused(t *testing.T) {
	root := t.TempDir()
	real := realGoFloorFixture(t, root)
	writeFloorFixture(t, root, real.gomod, "FROM scratch\nRUN echo no pinned toolchain here\n")

	err := checkGoToolchainFloor(root)
	if err == nil {
		t.Fatal("a Dockerfile with no ARG GO_VERSION pin passed the floor check")
	}
	if !strings.Contains(err.Error(), "no ARG GO_VERSION pin") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// short: one fixture tree per case.
//
// The CI half of the guard. Today's workflows follow go.mod
// (go-version-file), which is checked against the floor by construction and
// must keep passing; a workflow that starts pinning an explicit go-version
// below the floor is refused, naming the file and line, and one that pins at
// or above the floor passes. A pin the guard cannot read — an expression, a
// bare variable — is refused rather than waved through: this tick asks that
// explicit pins be checked, and a value that cannot be read cannot be checked.
func TestExplicitWorkflowGoPinsAreCheckedAgainstTheFloor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workflow string
		want     string // "" for meets; a fragment of the refusal otherwise
	}{
		{"following the module", "      - uses: actions/setup-go@v6\n        with:\n          go-version-file: go.mod\n", ""},
		// Relative to go.mod's floor, not spelled out: the floor rises (the
		// image's Go bump took it from 1.24.2 to 1.26.0), and a pin that was
		// "above" it when this was written is below it now.
		{"pinning above the floor", "          go-version: {ABOVE}\n", ""},
		{"pinning at the floor", "          go-version: '{FLOOR}'\n", ""},
		{"pinning below the floor", "          go-version: 1.22.0\n", "pins go 1.22.0"},
		{"pinning an unreadable value", "          go-version: stable\n", "not a plain dotted go version"},
		{"pinning an expression", "          go-version: ${{ vars.GO }}\n", "not a plain dotted go version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			real := realGoFloorFixture(t, root)
			writeFloorFixture(t, root, real.gomod, real.dockerfile)
			if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
				t.Fatal(err)
			}
			floor, err := moduleGoFloor(real.gomod)
			if err != nil {
				t.Fatal(err)
			}
			workflow := strings.NewReplacer("{FLOOR}", floor, "{ABOVE}", aboveGoFloor(t, floor)).Replace(tc.workflow)
			if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte(workflow), 0o644); err != nil {
				t.Fatal(err)
			}

			err = checkGoToolchainFloor(root)
			if tc.want == "" {
				if err != nil {
					t.Errorf("a workflow with %q was refused: %v", strings.TrimSpace(strings.SplitN(workflow, "go-version", 2)[1]), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a workflow with %q passed a %s floor", workflow, floor)
			}
			if !strings.Contains(err.Error(), "ci.yml:") {
				t.Errorf("the refusal does not name the workflow file and line: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal says %q, want it to say %q", err.Error(), tc.want)
			}
		})
	}
}

// realGoFloorFixture reads this tree's real go.mod and vendored image — the
// fixtures above must reproduce the real shapes, not a simplified one that
// passes because it never had the hard parts — and stages the go.mod alone
// into root.
func realGoFloorFixture(t *testing.T, root string) (real struct{ gomod, dockerfile string }) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "image"), 0o755); err != nil {
		t.Fatal(err)
	}

	gomod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("reading the real go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), gomod, 0o644); err != nil {
		t.Fatal(err)
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "image", sandboxDockerfileName))
	if err != nil {
		t.Fatalf("reading the real image's Dockerfile: %v", err)
	}
	return struct{ gomod, dockerfile string }{string(gomod), string(dockerfile)}
}

// writeFloorFixture stages a Dockerfile beside the go.mod already in root.
func writeFloorFixture(t *testing.T, root, gomod, dockerfile string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "image", sandboxDockerfileName), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
}

// aboveGoFloor is a Go version one minor release above floor.
func aboveGoFloor(t *testing.T, floor string) string {
	t.Helper()
	fields := strings.Split(floor, ".")
	if len(fields) < 2 {
		t.Fatalf("go.mod's floor %q has no minor version", floor)
	}
	minor, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("go.mod's floor %q: %v", floor, err)
	}
	return fields[0] + "." + strconv.Itoa(minor+1) + ".0"
}
