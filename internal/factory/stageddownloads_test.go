package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The image's downloads must survive a dropped connection (tick jge). An
// operator on a low-bandwidth link watched five consecutive builds die, each
// at whichever download happened to be largest — mise's 116 MB failed three
// times running with "curl: (56) Send failure: Broken pipe" — while the same
// URL fetched fine in a plain container given a clean window.
//
// The flag that matters is `-C -`. Retrying without resuming restarts a 116 MB
// transfer from zero, which on a link that drops every 40 MB never finishes
// however many retries it is given.

func stagedWith(t *testing.T, dockerfile string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sandboxDockerfileName), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("writing the staged Dockerfile: %v", err)
	}
	return dir
}

const twoDownloads = `FROM base
ARG GO_VERSION=1.24.11
RUN set -euo pipefail; \
    curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"; \
    echo "sum  /tmp/go.tar.gz" | sha256sum -c -
RUN set -euo pipefail; \
    curl -fsSL -o /tmp/mise "https://github.com/jdx/mise/releases/download/v1/mise"; \
    install -m 0755 /tmp/mise /usr/local/bin/mise
`

func TestEveryDownloadResumesRatherThanRestarting(t *testing.T) {
	dir := stagedWith(t, twoDownloads)

	found, err := MakeSandboxDownloadsResumable(dir)
	if err != nil {
		t.Fatalf("making the staged downloads resumable: %v", err)
	}
	if found != 2 {
		t.Fatalf("rewrote %d downloads, want 2", found)
	}

	data, err := os.ReadFile(filepath.Join(dir, sandboxDockerfileName))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	got := string(data)

	// -C - is the flag this tick exists for: without it a retry restarts.
	if n := strings.Count(got, "-C -"); n != 2 {
		t.Errorf("%d downloads resume, want 2 — a retry that does not resume restarts a 116 MB transfer from zero:\n%s", n, got)
	}
	if n := strings.Count(got, "--retry-all-errors"); n != 2 {
		t.Errorf("%d downloads retry on transport errors, want 2 — a broken pipe is not in curl's default retry set:\n%s", n, got)
	}
	// The URLs and the digest checks must survive untouched: a rewrite that
	// dropped a sha256sum would trade a failing build for a silently wrong one.
	if !strings.Contains(got, `"https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"`) {
		t.Errorf("the go URL did not survive the rewrite:\n%s", got)
	}
	if n := strings.Count(got, "sha256sum -c -"); n != 1 {
		t.Errorf("the digest check did not survive the rewrite:\n%s", got)
	}
}

func TestASecondRewriteIsRefusedRatherThanDoublingTheFlags(t *testing.T) {
	dir := stagedWith(t, twoDownloads)

	if _, err := MakeSandboxDownloadsResumable(dir); err != nil {
		t.Fatalf("first rewrite: %v", err)
	}
	_, err := MakeSandboxDownloadsResumable(dir)
	if err == nil {
		t.Fatal("a second rewrite was accepted; the staged Dockerfile is written fresh every deploy, so a second call means the staging order is wrong")
	}
	if !strings.Contains(err.Error(), "already fetches resumably") {
		t.Errorf("refusal does not say what happened: %v", err)
	}
}

func TestADockerfileWithNoDownloadsIsARefusal(t *testing.T) {
	dir := stagedWith(t, "FROM base\nRUN echo hello\n")

	_, err := MakeSandboxDownloadsResumable(dir)
	if err == nil {
		t.Fatal("a Dockerfile with nothing to rewrite was accepted; the image's fetch form changing must be a stop, not a silent no-op")
	}
	if !strings.Contains(err.Error(), "fetch form has changed") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
}

// The real image is the fixture that matters: a rewrite that works on a
// hand-written sample and not on the vendored Dockerfile is worth nothing.
func TestTheRealImageIsRewritten(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	real, err := os.ReadFile(filepath.Join(root, "image", sandboxDockerfileName))
	if err != nil {
		t.Skipf("no vendored image to check: %v", err)
	}

	dir := stagedWith(t, string(real))
	found, err := MakeSandboxDownloadsResumable(dir)
	if err != nil {
		t.Fatalf("rewriting the real image: %v", err)
	}
	if found < 5 {
		t.Errorf("rewrote %d downloads in the real image, want at least 5 (go, uv, bun, mise, omp)", found)
	}

	data, err := os.ReadFile(filepath.Join(dir, sandboxDockerfileName))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if n := strings.Count(string(data), "-C -"); n != found {
		t.Errorf("%d of %d downloads resume", n, found)
	}
}

// A stalled connection must become a FAILURE, or the retries above are
// unreachable (tick jge, second cut). The first version of this fix made
// dropped transfers survivable and stalled ones permanent: a deploy sat on one
// for eight hours overnight and produced nothing, which is worse than the
// plain curl it replaced, because that would have died and let the build say so.
func TestAStalledTransferIsFailedSoItCanBeRetried(t *testing.T) {
	dir := stagedWith(t, twoDownloads)

	if _, err := MakeSandboxDownloadsResumable(dir); err != nil {
		t.Fatalf("making the staged downloads resumable: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, sandboxDockerfileName))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	got := string(data)

	if n := strings.Count(got, "--speed-limit"); n != 2 {
		t.Errorf("%d downloads bound their minimum speed, want 2 — --retry only fires on a FAILURE, and a connection that stalls but stays open never fails:\n%s", n, got)
	}
	if n := strings.Count(got, "--speed-time"); n != 2 {
		t.Errorf("%d downloads bound how long a stall may last, want 2:\n%s", n, got)
	}
}
