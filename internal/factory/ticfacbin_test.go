package factory

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The report checker the worker prompts name (tick 4m6) ships in the sandbox
// image: it is the executor binary the deploy already stages onto PATH, and
// every cloud role prompt tells the worker to run it on its report.
func TestTheReportCheckerShipsInTheSandboxImageAndEveryCloudPromptNamesIt(t *testing.T) {
	t.Parallel()
	staged := false
	for _, binary := range ticfacStagedBinaries {
		staged = staged || binary == subprocess.LintCommandName
	}
	if !staged {
		t.Fatalf("%s is not among the staged binaries %v", subprocess.LintCommandName, ticfacStagedBinaries)
	}
	if !strings.Contains(ticfacInstallBlock+ticfacInstallSteps, "/usr/local/bin/"+subprocess.LintCommandName) {
		t.Fatalf("the install block does not put %s on PATH", subprocess.LintCommandName)
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"implement-tick", "review-epic", "closeout-epic"} {
		raw, err := os.ReadFile(filepath.Join(root, cloudProfilesContextName, role+".md"))
		if err != nil {
			t.Fatal(err)
		}
		want := subprocess.LintCommandName + " lint-report RESULT-<tick id>.md --role " + role
		if !strings.Contains(string(raw), want) || !strings.Contains(string(raw), "```findings v2") {
			t.Errorf("the cloud %s prompt does not name the report check (%q) and the v2 findings block", role, want)
		}
	}
}

// ticfac in the orchestrator image (tick prs).
//
// The acceptance is that `ticfac version` answers from inside the container,
// and the two halves of that are covered separately here: the binaries are
// really Linux binaries of the right architecture (the expensive test, which
// compiles), and the staged Dockerfile really installs and verifies them (the
// cheap tests, which do not).

// stagedDockerfile writes the embedded Dockerfile into a temp dir, the way
// MaterializeSandbox does, and returns the directory.
func stagedDockerfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	return dir
}

// fakeSums stages placeholder files for every binary/arch pair and returns
// their real digests, so the Dockerfile half can be tested without a compiler.
func fakeSums(t *testing.T, dir string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	for _, binary := range ticfacStagedBinaries {
		for _, arch := range ticfacStagedArches {
			name := stagedBinaryName(binary, arch)
			body := []byte("placeholder " + name + "\n")
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o755); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			sums[name] = hex.EncodeToString(sum[:])
		}
	}
	return sums
}

// short: writes one Dockerfile in a temp dir and reads it back.
//
// The whole of what the container needs, asserted on the artifact the build
// actually reads: the version and the four checksums in the version block at
// the top, a COPY for BOTH binaries selected by TARGETARCH, and the assertion
// that makes `ticfac version` a build failure rather than a run-time surprise.
func TestSetSandboxTicfacPinsTeachesTheDockerfileToInstallTicfac(t *testing.T) {
	dir := stagedDockerfile(t)
	sums := fakeSums(t, dir)

	if err := SetSandboxTicfacPins(dir, "9.9.9", sums); err != nil {
		t.Fatalf("SetSandboxTicfacPins: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, sandboxDockerfileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		"\nARG TICFAC_VERSION=9.9.9\n",
		"\nARG TICFAC_SHA256_AMD64=" + sums[stagedBinaryName(ticfacBinary, "amd64")] + "\n",
		"\nARG TICFAC_SHA256_ARM64=" + sums[stagedBinaryName(ticfacBinary, "arm64")] + "\n",
		"\nARG TICFAC_EXEC_SHA256_AMD64=" + sums[stagedBinaryName(ticfacExecutorBinary, "amd64")] + "\n",
		"\nARG TICFAC_EXEC_SHA256_ARM64=" + sums[stagedBinaryName(ticfacExecutorBinary, "arm64")] + "\n",
		"\nCOPY --chmod=0755 ticfac-linux-${TARGETARCH} /usr/local/bin/ticfac\n",
		"\nCOPY --chmod=0755 ticfac-exec-subprocess-linux-${TARGETARCH} /usr/local/bin/ticfac-exec-subprocess\n",
		"/usr/local/bin/ticfac-exec-subprocess",
		`ticfac ${TICFAC_VERSION}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the staged Dockerfile does not carry %q", strings.TrimSpace(want))
		}
	}

	// The pins are declared after every other layer, just before the COPYs
	// that use them: they change on every deploy, and BuildKit keys each RUN
	// on every ARG in scope. Declared in the version block at the top (as they
	// were until 2026-09-30) they rebuilt tk, the toolchains and the batteries
	// — 22 of 46 layers, ~0.8 GB — on every deploy.
	pins := strings.Index(got, "ARG TICFAC_VERSION=")
	install := strings.Index(got, "COPY --chmod=0755 ticfac-linux-")
	lastRunAbove := strings.LastIndex(got[:max(pins, 0)], "\nRUN ")
	base := strings.Index(got, "ARG GO_VERSION=")
	if pins == -1 || install == -1 || base == -1 || lastRunAbove == -1 {
		t.Fatalf("the staged Dockerfile lost its shape:\n%s", got)
	}
	if pins < base {
		t.Errorf("the ticfac pins are in the version block at the top, where they bust every RUN's cache")
	}
	for _, other := range []string{"ARG TK_VERSION=", "ARG GO_VERSION="} {
		if i := strings.Index(got, other); i > pins {
			t.Errorf("%s is below the ticfac pins", other)
		}
	}
	if between := got[pins:install]; strings.Contains(between, "\nRUN ") {
		t.Errorf("a RUN sits between the ticfac pins and the install COPYs:\n%s", between)
	}
	if install < pins {
		t.Errorf("the ticfac install block is above its own pins")
	}
	if strings.Contains(got[:pins], "${TICFAC_") {
		t.Errorf("something above the ticfac pins already refers to them")
	}

	// TARGETARCH has to be re-declared with NO default before the COPYs.
	// Measured against docker 29.4.0: a stage declaring `ARG TARGETARCH=amd64`
	// — which image/Dockerfile above does — reports amd64 even for
	// `--platform linux/arm64`, so without this line the image would carry the
	// wrong architecture's orchestrator. A bare re-declaration restores
	// BuildKit's real value.
	redeclare := strings.LastIndex(got[:install], "\nARG TARGETARCH\n")
	if redeclare == -1 {
		t.Errorf("the install block does not re-declare ARG TARGETARCH without a default before its COPYs")
	}
}

// short: one Dockerfile in a temp dir.
//
// MaterializeSandbox rewrites the staged Dockerfile from the embedded tree on
// every deploy, so a second insertion means the staging ran in the wrong
// order — a duplicated ARG set the build would silently accept is exactly the
// kind of drift the staged-context contract exists to make impossible.
func TestSetSandboxTicfacPinsRefusesToPinTwice(t *testing.T) {
	dir := stagedDockerfile(t)
	sums := fakeSums(t, dir)

	if err := SetSandboxTicfacPins(dir, "1.0.0", sums); err != nil {
		t.Fatalf("SetSandboxTicfacPins: %v", err)
	}
	err := SetSandboxTicfacPins(dir, "1.0.0", sums)
	if err == nil {
		t.Fatal("SetSandboxTicfacPins pinned ticfac twice into one Dockerfile")
	}
	if !strings.Contains(err.Error(), "already pins ticfac") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// short: no compiler, no Docker.
//
// An image pinned to a checksum of something that was never staged is an image
// whose build fails at the sha256sum line with nothing useful to say. The
// refusal names the missing file instead.
func TestSetSandboxTicfacPinsRefusesUnstagedBinaries(t *testing.T) {
	dir := stagedDockerfile(t)
	sums := fakeSums(t, dir)
	delete(sums, stagedBinaryName(ticfacExecutorBinary, "arm64"))

	err := SetSandboxTicfacPins(dir, "1.0.0", sums)
	if err == nil {
		t.Fatal("SetSandboxTicfacPins pinned an image to a binary that was never staged")
	}
	if !strings.Contains(err.Error(), stagedBinaryName(ticfacExecutorBinary, "arm64")) {
		t.Errorf("the refusal does not name the missing binary: %v", err)
	}
}

// short: reads one go.mod.
func TestTicfacModuleDirFindsTheCheckoutAndRefusesAnythingElse(t *testing.T) {
	dir, err := TicfacModuleDir()
	if err != nil {
		t.Fatalf("TicfacModuleDir from the checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", ticfacBinary)); err != nil {
		t.Errorf("TicfacModuleDir returned %s, which has no cmd/%s: %v", dir, ticfacBinary, err)
	}

	restore := chdir(t, t.TempDir())
	defer restore()
	if _, err := TicfacModuleDir(); err == nil {
		t.Fatal("TicfacModuleDir found a ticfac checkout outside one")
	} else if !strings.Contains(err.Error(), "inside a ticfac checkout") {
		t.Errorf("the stop does not say where to run the deploy from: %v", err)
	}
}

// The expensive half, and the one the tick turns on: the staged files are real
// Linux binaries for the architecture they are named for, and both commands
// are there. Skipped under -short (it compiles this module four times); CI's
// full run is the verdict.
func TestStageTicfacBinariesCrossCompilesBothCommandsForBothArches(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles this module four times")
	}
	moduleDir, err := TicfacModuleDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	sums, err := StageTicfacBinaries(context.Background(), dir, moduleDir, "7.7.7")
	if err != nil {
		t.Fatalf("StageTicfacBinaries: %v", err)
	}

	wantMachine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	for _, binary := range ticfacStagedBinaries {
		for _, arch := range ticfacStagedArches {
			name := stagedBinaryName(binary, arch)
			path := filepath.Join(dir, name)
			f, err := elf.Open(path)
			if err != nil {
				t.Errorf("%s is not an ELF binary — the container could not execute it: %v", name, err)
				continue
			}
			if f.Machine != wantMachine[arch] {
				t.Errorf("%s is built for %v, not %v", name, f.Machine, wantMachine[arch])
			}
			f.Close()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if got := hex.EncodeToString(sum[:]); got != sums[name] {
				t.Errorf("the recorded sha256 for %s is not the file's: the image would be pinned to a digest sha256sum -c cannot match", name)
			}
		}
	}
}
