package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// The orchestrator image fetches its runtimes over the network: Go, uv, bun,
// mise and omp, together several hundred megabytes. Each fetch is a plain
// `curl -fsSL`, which gives up the moment the connection drops — and a dropped
// transfer fails the whole layer, so the next attempt starts that download
// again from zero.
//
// On a good link that is invisible. On a poor one it is the difference between
// a deploy that takes a while and a deploy that CANNOT COMPLETE (tick jge): an
// operator on a low-bandwidth connection watched five consecutive builds die,
// each at whichever download happened to be largest, with mise's 116 MB
// failing three times running at `curl: (56) Send failure: Broken pipe`. The
// same URL fetched fine in a plain container when it got a clean window; the
// build simply never got one.
//
// So the staged copy's downloads are rewritten to survive a drop instead of
// dying on it. This is the same mechanism SetSandboxTkPins and
// SetSandboxTicfacPins use — image/ is vendored from ticks and never edited
// here, while the STAGED copy is the deploy's to specialise.
//
// This belongs upstream in ticks eventually, where it would help everyone
// building that image on a bad connection rather than only this deploy. Until
// then, the rewrite lives where the deploy already rewrites.

// resumableCurlFlags make a download survive a dropped connection AND a
// stalled one.
//
//	--retry 10              try again, rather than failing the layer
//	--retry-all-errors      including the transport failures that are the
//	                        whole problem here; without it curl retries only
//	                        a documented subset and a broken pipe is not in it
//	--retry-delay 2         a pause, so a flapping link is not hammered
//	-C -                    RESUME. Without it each retry restarts from zero,
//	                        and a 116 MB file on a link that drops every 40 MB
//	                        never finishes however many times it is retried.
//	--speed-limit 1024      \ a transfer moving slower than 1 KB/s for two
//	--speed-time 120        / minutes is treated as FAILED, which is what makes
//	                        the retries above reachable.
//
// That last pair is not a refinement, it is the other half of the fix. --retry
// only fires when a transfer FAILS. A connection that stalls but stays open —
// no bytes, no error — never fails, so it never retries: the first version of
// this made dropped transfers survivable and stalled ones PERMANENT. A deploy
// left running overnight sat on one for eight hours and produced nothing,
// which is strictly worse than the plain curl it replaced, because that would
// at least have died and let the build report it.
//
// A stall must therefore be turned into a failure before the retry machinery
// can do anything about it. 1 KB/s for 120s is chosen to be unambiguous: a
// real transfer on a bad link still moves far faster than that, so this fires
// on dead connections rather than on slow ones.
const resumableCurlFlags = "--retry 10 --retry-all-errors --retry-delay 2 -C - --speed-limit 1024 --speed-time 120"

// curlDownload matches the image's download form: `curl -fsSL -o <path> "<url>"`.
//
// Deliberately narrow. It matches the flags the image actually writes rather
// than any curl, so a line that does something else is left alone and a line
// that has already been rewritten does not match twice.
var curlDownload = regexp.MustCompile(`curl -fsSL -o `)

// curlAlreadyResumable reports the marker that makes a second rewrite a
// refusal rather than a doubling of flags.
var curlAlreadyResumable = regexp.MustCompile(`curl -fsSL --retry `)

// MakeSandboxDownloadsResumable rewrites the staged Dockerfile's downloads so
// a dropped connection resumes instead of failing the build (tick jge).
//
// Refuses rather than guesses: a staged Dockerfile with no downloads to
// rewrite, or one already rewritten, is a staging-order mistake and is
// reported as one. MaterializeSandbox writes the staged copy fresh on every
// deploy, so a second insertion cannot be a legitimate second call.
func MakeSandboxDownloadsResumable(dir string) (int, error) {
	configPath := filepath.Join(dir, sandboxDockerfileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", configPath, err)
	}

	if curlAlreadyResumable.Match(data) {
		return 0, fmt.Errorf("%s already fetches resumably — the staged Dockerfile is written fresh by MaterializeSandbox on every deploy, so a second rewrite means the staging order is wrong", configPath)
	}

	found := len(curlDownload.FindAll(data, -1))
	if found == 0 {
		return 0, fmt.Errorf("%s has no `curl -fsSL -o` download to make resumable — the image's fetch form has changed and this rewrite no longer applies", configPath)
	}

	rewritten := curlDownload.ReplaceAll(data, []byte("curl -fsSL "+resumableCurlFlags+" -o "))
	if err := os.WriteFile(configPath, rewritten, 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", configPath, err)
	}
	return found, nil
}
