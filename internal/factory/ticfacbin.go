package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ticfac itself in the orchestrator image (tick prs).
//
// The image already carries tk, git, Go, ripgrep and a real filesystem, and
// the Run Workflow starts /usr/local/bin/ticks-orchestrator inside it. It did
// not carry ticfac, which is the gap epic yoh turns on: the orchestrator is
// supposed to run WHERE GIT IS, and it cannot run there if the binary is not
// there.
//
// WHY THE DEPLOY CROSS-COMPILES IT RATHER THAN THE IMAGE INSTALLING IT.
// tk is installed with `go install <module>@<ref>` inside the build, which is
// why Go is in the image. That mechanism does not transfer: ticfac is a
// PRIVATE repository, so a build-time `go install` would need credentials
// inside the Docker build — a secret in a build context, for a binary the
// deploying machine has already compiled once. Cross-compiling on the
// deploying machine needs no credentials and gives a property stronger than
// any pin: the ticfac in the container and the bundle that container was
// deployed from are the same commit by construction.
//
// WHY THE BLOCK IS APPENDED TO THE STAGED DOCKERFILE AND NOT COMMITTED.
// image/ is ticfac's VENDORED copy of ticks' cloud/sandbox tree. sandbox.pin.json
// states the ownership rule outright — "ticfac never edits a file under image/"
// — and CI enforces it from both ends: `go run ./cmd/sandbox check` verifies
// the vendored bytes against the pin offline, and `go run ./cmd/sandbox
// verify-upstream` fetches ticks at the pinned ref and compares byte for byte.
// Committing these lines under image/ would fail both. Putting them UPSTREAM in
// ticks would be worse: ticks' own build context has no ticfac binaries to
// COPY, so every ticks image build would break on them.
//
// So the block goes where the deploy's other image edits already go. The tk
// pins are rewritten in the STAGED copy by SetSandboxTkPins for exactly this
// reason, and MaterializeSandbox re-writes that copy from the embedded tree on
// every deploy — which means the append cannot accumulate and the vendored
// tree is never touched.

const (
	// ticfacBinary is the orchestrator itself.
	ticfacBinary = "ticfac"

	// ticfacExecutorBinary is the local subprocess executor. It is NOT
	// optional: a run refuses to start unless it sits beside ticfac
	// (internal/reconcile/dispatch.go), correctly — a run without it would
	// start jobs nothing is watching. Forgetting it cost a restart cycle on
	// the 2026-09-20 gate run, so it is staged by the same loop as ticfac
	// rather than by a second one somebody could fail to add to.
	ticfacExecutorBinary = "ticfac-exec-subprocess"

	// ticfacModulePath is what the module the deploy compiles must declare.
	ticfacModulePath = "github.com/pengelbrecht/ticfac"

	// ticfacVersionSymbol is the ldflags target for the version the container
	// reports. It is the same symbol the release build stamps, so `ticfac
	// version` inside the container answers the way it does outside it.
	ticfacVersionSymbol = ticfacModulePath + "/internal/cli.Version"
)

// ticfacStagedBinaries are the commands staged into the image context, in the
// order they are built.
var ticfacStagedBinaries = []string{ticfacBinary, ticfacExecutorBinary}

// ticfacStagedArches are the architectures staged. BOTH, always: the
// Dockerfile carries amd64 and arm64 pins for every other battery, so the
// image can be built for either, and a context that staged only one would
// turn a `--platform` the operator is entitled to into a COPY that cannot
// resolve. Selecting between them is the Dockerfile's job (TARGETARCH), not a
// guess made here about a build that has not started.
var ticfacStagedArches = []string{"amd64", "arm64"}

// stagedBinaryName is the file one command/arch pair is staged under, and the
// name the Dockerfile's COPY spells with ${TARGETARCH} substituted.
func stagedBinaryName(binary, arch string) string {
	return binary + "-linux-" + arch
}

// goProbeTimeout bounds the "is a Go toolchain here" question, for the same
// reason dockerProbeTimeout bounds its own: the answer is a local exec, so
// anything near this bound is already a fault.
const goProbeTimeout = 30 * time.Second

// ticfacBuildTimeout bounds the four cross-compiles together. A cold build of
// this module for a foreign GOARCH is tens of seconds; ten minutes is room for
// a slow machine and still refuses a toolchain that has wedged.
const ticfacBuildTimeout = 10 * time.Minute

// requireGo proves a Go toolchain is installed AND answers, and returns the
// version that answered — the same "validate WHAT answered" rule requireDocker
// and the wrangler probe follow.
func requireGo(ctx context.Context) (string, error) {
	resolved, err := exec.LookPath("go")
	if err != nil {
		return "", &PrerequisiteError{
			Missing: "Go",
			Detail: "`ticfac factory deploy` cross-compiles ticfac and " + ticfacExecutorBinary + " for\n" +
				"Linux into the orchestrator image's build context — the container has to carry\n" +
				"the orchestrator, and ticfac is a private repository the Docker build cannot\n" +
				"clone. Install Go (https://go.dev/dl/) and run `ticfac factory deploy` again.",
			Err: err,
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, goProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, resolved, "version").CombinedOutput()
	version := strings.TrimSpace(lastNonEmptyLine(string(out)))
	if err != nil || version == "" {
		return "", &PrerequisiteError{
			Missing: "a working Go toolchain",
			Detail: "`go` is on PATH but `go version` did not answer, so the orchestrator's own\n" +
				"binaries cannot be built for the image.\n" + strings.TrimSpace(string(out)),
			Err: err,
		}
	}
	return version, nil
}

// TicfacModuleDir locates the ticfac checkout the deploy compiles from, by
// walking up from the working directory to the module root and proving the
// go.mod found there is ticfac's.
//
// This is the one real constraint the cross-compile adds: `ticfac factory
// deploy` has to run from inside a ticfac checkout. That is where it is run
// from — the deploying machine has just built the binary — and the
// alternative, a `go install` from the private module path, needs credentials
// inside the Docker build. The requirement is stated as a stop with a remedy
// rather than discovered as a build failure with none.
func TicfacModuleDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	start := dir
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, readErr := os.ReadFile(gomod); readErr == nil {
			if modulePathOf(data) == ticfacModulePath {
				return dir, nil
			}
			return "", ticfacSourceMissing(start,
				fmt.Errorf("%s declares module %q, not %q", gomod, modulePathOf(data), ticfacModulePath))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ticfacSourceMissing(start, fmt.Errorf("no go.mod above %s", start))
		}
		dir = parent
	}
}

func ticfacSourceMissing(start string, err error) error {
	return &PrerequisiteError{
		Missing: "a ticfac checkout to build the orchestrator's binaries from",
		Detail: "The orchestrator image carries ticfac and " + ticfacExecutorBinary + ", and the deploy\n" +
			"cross-compiles them for Linux because ticfac is a private repository the Docker\n" +
			"build cannot clone. That needs the source: run `ticfac factory deploy` from\n" +
			"inside a ticfac checkout.\n" +
			"Looked upwards from " + start + ".",
		Err: err,
	}
}

// modulePattern matches the module line of a go.mod.
var modulePattern = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

func modulePathOf(gomod []byte) string {
	m := modulePattern.FindSubmatch(gomod)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// StageTicfacBinaries cross-compiles ticfac and ticfac-exec-subprocess for
// linux/amd64 and linux/arm64 into the staged image context, and returns the
// sha256 of each staged file keyed by its staged name.
//
// It must run AFTER MaterializeSandbox. That is not an ordering preference: it
// is the staged-context contract. MaterializeSandbox rewrites every file the
// embedded tree ships and PRUNES everything else under the directory, so
// binaries staged by a previous deploy are deleted there and re-staged here —
// which is what keeps a stale binary from a previous ticfac build out of the
// image. Staging first would simply delete them again.
//
// CGO is off, so the binaries are static and run on the image's base with no
// libc question, and -trimpath matches how the release build is produced.
func StageTicfacBinaries(ctx context.Context, dir, moduleDir, version string) (map[string]string, error) {
	if !validTkPin.MatchString(version) {
		return nil, fmt.Errorf("%q is not a usable ticfac version to stamp the image's binaries with", version)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("preparing %s: %w", dir, err)
	}

	buildCtx, cancel := context.WithTimeout(ctx, ticfacBuildTimeout)
	defer cancel()

	sums := make(map[string]string, len(ticfacStagedBinaries)*len(ticfacStagedArches))
	for _, binary := range ticfacStagedBinaries {
		for _, arch := range ticfacStagedArches {
			name := stagedBinaryName(binary, arch)
			dest := filepath.Join(dir, name)
			cmd := exec.CommandContext(buildCtx, goBin, "build",
				"-trimpath",
				"-ldflags", "-s -w -X "+ticfacVersionSymbol+"="+version,
				"-o", dest,
				"./cmd/"+binary)
			cmd.Dir = moduleDir
			cmd.Env = append(os.Environ(),
				"GOOS=linux",
				"GOARCH="+arch,
				"CGO_ENABLED=0",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf(
					"cross-compiling %s for linux/%s into the orchestrator image context failed.\n"+
						"The container cannot run an epic without it.\n%s%w",
					binary, arch, indent(strings.TrimRight(string(out), "\n"), "  "), err)
			}
			sum, err := fileSHA256(dest)
			if err != nil {
				return nil, err
			}
			sums[name] = sum
		}
	}
	return sums, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the staged %s: %w", filepath.Base(path), err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// tkModuleArgPattern is the last line of the Dockerfile's tk pins, and the
// anchor the ticfac pins are inserted after: they belong IN the version block
// at the top, beside the pins they move with, not appended somewhere a
// rebuild's diff would not show them together.
var tkModuleArgPattern = regexp.MustCompile(`(?m)^ARG TK_MODULE=\S*$`)

// ticfacPinMarker identifies the inserted pin block, so a second application
// is a refusal rather than a duplicate set of ARGs.
const ticfacPinMarker = "ARG TICFAC_VERSION="

// SetSandboxTicfacPins rewrites the STAGED Dockerfile so it carries ticfac:
// the version and the four checksums pinned in the version block at the top,
// and the install block that copies the staged binaries onto PATH at the end.
//
// Same contract as SetSandboxTkPins — the staged copy is edited and the
// vendored tree is not — with one addition it cannot share: the vendored
// Dockerfile has no ticfac lines to rewrite, so this inserts them.
func SetSandboxTicfacPins(dir, version string, sums map[string]string) error {
	if !validTkPin.MatchString(version) {
		return fmt.Errorf("%q is not a usable TICFAC_VERSION pin", version)
	}
	var missing []string
	for _, binary := range ticfacStagedBinaries {
		for _, arch := range ticfacStagedArches {
			name := stagedBinaryName(binary, arch)
			if !validSHA256.MatchString(sums[name]) {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("no sha256 for %s — the image may not be pinned to binaries that were never staged",
			strings.Join(missing, ", "))
	}

	configPath := filepath.Join(dir, sandboxDockerfileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}
	if strings.Contains(string(data), ticfacPinMarker) {
		return fmt.Errorf("%s already pins ticfac — the staged Dockerfile is written fresh by MaterializeSandbox on every deploy, so a second insertion means the staging order is wrong", configPath)
	}
	anchor := tkModuleArgPattern.FindIndex(data)
	if anchor == nil {
		return fmt.Errorf("%s declares no ARG TK_MODULE to anchor the ticfac pins after", configPath)
	}

	var b strings.Builder
	b.Write(data[:anchor[1]])
	b.WriteString("\n\n")
	b.WriteString(ticfacPinBlock(version, sums))
	b.Write(data[anchor[1]:])
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(ticfacInstallBlock)

	if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}
	return nil
}

// validSHA256 is a hex sha256 digest and nothing else — the same "reject
// rather than escape" rule validTkPin applies to a pin.
var validSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ticfacPinBlock(version string, sums map[string]string) string {
	return "# ticfac's own pins, inserted here by `ticfac factory deploy` (tick prs).\n" +
		"# TICFAC_VERSION is what `ticfac version` reports inside the container, and the\n" +
		"# checksums are of the binaries the deploy cross-compiled into this context — so a\n" +
		"# rebuild is a diff of this block for ticfac exactly as it is for everything above.\n" +
		"ARG TICFAC_VERSION=" + version + "\n" +
		"ARG TICFAC_SHA256_AMD64=" + sums[stagedBinaryName(ticfacBinary, "amd64")] + "\n" +
		"ARG TICFAC_SHA256_ARM64=" + sums[stagedBinaryName(ticfacBinary, "arm64")] + "\n" +
		"ARG TICFAC_EXEC_SHA256_AMD64=" + sums[stagedBinaryName(ticfacExecutorBinary, "amd64")] + "\n" +
		"ARG TICFAC_EXEC_SHA256_ARM64=" + sums[stagedBinaryName(ticfacExecutorBinary, "arm64")] + "\n"
}

// ticfacInstallBlock is appended to the staged Dockerfile. It is last on
// purpose: everything above it is identical across deploys of the same
// vendored tree, so the binaries — the only layer that changes every deploy —
// invalidate nothing but themselves.
//
// `ARG TARGETARCH` is RE-DECLARED with no default, and that is load-bearing
// rather than tidy. The declaration at the top of the vendored Dockerfile
// carries `=amd64`, and a default SHADOWS the value BuildKit sets: measured
// against docker 29.4.0, `docker build --platform linux/arm64` on a stage
// declaring `ARG TARGETARCH=amd64` reports amd64 and resolves
// `COPY blob-linux-${TARGETARCH}` to the amd64 file. A bare re-declaration
// restores the real value — verified in the same way, arm64 selecting the
// arm64 file and amd64 the amd64 one. (That the DEFAULT is wrong for every
// battery above is a pre-existing fault in the vendored tree, fixable only in
// ticks; this block does not inherit it.)
const ticfacInstallBlock = `
# ---------------------------------------------------------------------------
# ticfac, the orchestrator itself, CROSS-COMPILED INTO THIS BUILD CONTEXT BY
# THE DEPLOY. This block is not in the committed image/Dockerfile and cannot
# be: image/ is ticfac's vendored copy of ticks' cloud/sandbox tree
# (sandbox.pin.json), which ticfac never edits — and ticks, whose build context
# holds no ticfac binaries, could not carry these COPY lines either.
#
# Not ` + "`go install`" + `d from the module path the way tk is above: ticfac is a
# PRIVATE repository, so that would need credentials inside the Docker build.
# Compiling on the deploying machine needs none, and makes the ticfac in this
# image and the bundle it was deployed from the same commit by construction.
#
# BOTH binaries. A run refuses to start unless ticfac-exec-subprocess sits
# beside ticfac — correctly: a run without it would start jobs nothing is
# watching.
#
# TARGETARCH is re-declared with NO DEFAULT deliberately. A default shadows the
# value BuildKit sets, and the declaration at the top of this file has one — so
# without this line a --platform build would COPY the wrong architecture's
# binary and the image would carry an orchestrator that cannot execute.
#
# COPY --chmod straight onto PATH, rather than copying to /tmp and installing:
# a RUN that rewrites the file would put a second copy of 15 MB of binary in a
# second layer, and the image would grow by twice what it carries. The layer
# below only READS them, so it adds nothing.
ARG TARGETARCH
COPY --chmod=0755 ticfac-linux-${TARGETARCH} /usr/local/bin/ticfac
COPY --chmod=0755 ticfac-exec-subprocess-linux-${TARGETARCH} /usr/local/bin/ticfac-exec-subprocess
RUN set -euo pipefail; \
    case "${TARGETARCH}" in \
      amd64) ticfac_sum="${TICFAC_SHA256_AMD64}"; exec_sum="${TICFAC_EXEC_SHA256_AMD64}" ;; \
      arm64) ticfac_sum="${TICFAC_SHA256_ARM64}"; exec_sum="${TICFAC_EXEC_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    echo "${ticfac_sum}  /usr/local/bin/ticfac" | sha256sum -c -; \
    echo "${exec_sum}  /usr/local/bin/ticfac-exec-subprocess" | sha256sum -c -; \
    test -x /usr/local/bin/ticfac; \
    test -x /usr/local/bin/ticfac-exec-subprocess

# Green-start discipline, the same one the batteries above answer to: prove the
# orchestrator answers, and that it is the version this image says it is.
RUN set -euo pipefail; \
    reported="$(timeout 60 ticfac version | sed -n 1p)"; \
    if [[ $reported != "ticfac ${TICFAC_VERSION}" ]]; then \
      echo "the ticfac in this image reports ${reported}, but the image pins ${TICFAC_VERSION}" >&2; \
      exit 1; \
    fi; \
    echo "$reported"

# The entrypoint is NOT changed here (tick prs stops at presence; tick hn0
# makes the orchestrator run ticfac).
ENV TICFAC_VERSION=${TICFAC_VERSION}
`

// InstallTicfacInSandbox is the whole of what a deploy needs: a Go toolchain
// and a ticfac checkout proved to exist, the binaries staged into the image
// context, and the staged Dockerfile taught to install them. It returns the
// staged file names, sorted, so the deploy can say what it put there.
//
// Its two prerequisites are settled here rather than up with wrangler and
// Docker for a reason that survives a reader: the deploy stages the image
// context BEFORE it creates anything in the account, so a stop from here is
// still a stop before the first `create` — and a probe that only matters to
// this step reads better beside it than three screens away.
func InstallTicfacInSandbox(ctx context.Context, dir, version string) ([]string, error) {
	moduleDir, err := TicfacModuleDir()
	if err != nil {
		return nil, err
	}
	if _, err := requireGo(ctx); err != nil {
		return nil, err
	}
	sums, err := StageTicfacBinaries(ctx, dir, moduleDir, version)
	if err != nil {
		return nil, err
	}
	if err := SetSandboxTicfacPins(dir, version, sums); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
