// Package runsignal is the orchestrator's half of the completion signal
// (tick 7eq): when a `ticfac run-epic` finishes inside an orchestrator
// container, it POSTs the factory's done door so its Run Workflow learns the
// finish without polling for it.
//
// A container invoking a Workflow binding DIRECTLY is unverified platform
// ground, so the route through the Worker — which provably holds the binding —
// is the design. The door turns the POST into instance.sendEvent(), the
// platform buffers the event, and the Workflow's next wait returns immediately.
//
// THE BRANCH IS THE TRUTH. The signal is an optimisation over the cadence,
// never a verdict: a container can die after finishing and before its callback
// lands, which is exactly the gap event buffering does not close. So Post never
// returns an error and never changes a run's exit code — a signal that cannot
// be delivered is said to the log and swallowed, and the run's own durable
// records remain the only evidence anybody reads.
package runsignal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The door's path, spelled as the Worker serves it (cloudflare/src/run-done.ts;
// the route table pins the two sides together). Not the event type — that is
// the Worker's vocabulary, and this client deliberately does not know it.
const donePath = "/api/done"

// The environment an orchestrator container is booted with. Both names are in
// image/README.md's entrypoint contract and exported by the staged entrypoint
// (internal/factory/ticfacentrypoint.go): the factory URL and the run's own
// gateway credential — the same token every other in-run door takes (D17), so
// an operator's revocation stops the signal along with the spending.
const (
	factoryURLEnv   = "TICKS_FACTORY_URL"
	factoryTokenEnv = "TICKS_FACTORY_TOKEN"
)

// A full commit, as `git ls-remote` prints it and the door accepts it.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// httpTimeout bounds the one POST a run makes. Short on purpose: the signal is
// sent by a container that has finished its work and is about to exit, so
// waiting on a slow door is spending the container's remaining lifetime on
// evidence nobody reads — the branch already said everything.
const httpTimeout = 10 * time.Second

// Signaller posts completion signals to one factory.
//
// A nil *Signaller is valid and does nothing: FromEnv returns nil for a local
// run (no factory configured), so every call site can post unconditionally
// the way it writes to the feed — the no-op IS the "this is not a cloud run"
// case, not a branch the caller has to take.
type Signaller struct {
	url   string
	token string
	log   io.Writer
	http  *http.Client
}

// New builds a Signaller for a factory URL and its run-scoped token. The URL's
// trailing slash is trimmed rather than validated: the door is best effort,
// and a malformed URL is a delivery failure, not a refusal the caller can act
// on. The log is where every outcome — delivered or not — is said.
func New(factoryURL, token string, log io.Writer) *Signaller {
	return &Signaller{
		url:   strings.TrimRight(strings.TrimSpace(factoryURL), "/"),
		token: strings.TrimSpace(token),
		log:   log,
		http:  &http.Client{Timeout: httpTimeout},
	}
}

// FromEnv reads the container's own factory configuration. Nil — the valid
// no-op — when either half is missing, which is what a local run looks like:
// nothing outside a cloud run knows the door, so nothing outside a cloud run
// posts to it.
func FromEnv(log io.Writer) *Signaller {
	url := strings.TrimSpace(os.Getenv(factoryURLEnv))
	token := strings.TrimSpace(os.Getenv(factoryTokenEnv))
	if url == "" || token == "" {
		return nil
	}
	return New(url, token, log)
}

// The signal payload: the branch and where it stands — never the work product.
// The platform caps an event payload at 1 MiB, and the Workflow reads nothing
// here as a verdict anyway; the head is the one durable fact worth carrying.
type signal struct {
	Branch string `json:"branch"`
	Head   string `json:"head,omitempty"`
}

// Done reports that the run has finished: it resolves the branch head from the
// REMOTE — the durable layer, not the local checkout the run is about to lose —
// and posts the door.
//
// Never returns anything. Every failure is logged and swallowed: the pushed
// branch is the source of truth, the Workflow's own cadence covers a lost
// signal, and a run's exit code must not depend on whether the news of its
// finish got through.
//
// An empty remote means "origin", matching run-epic's own default. A head that
// cannot be resolved (the run failed before pushing anything) is omitted
// rather than invented: the wake-up is the point, and the door accepts a
// branch on its own.
func (s *Signaller) Done(ctx context.Context, repo, remote, branch string) {
	if s == nil {
		return
	}
	if remote == "" {
		remote = "origin"
	}
	head, headErr := remoteHead(ctx, repo, remote, branch)
	if headErr != nil {
		// Said, never fatal: a branch that never landed is a real outcome of a
		// failed run, and the signal is still worth sending without a head.
		fmt.Fprintf(s.log, "completion signal: could not resolve %s on %s (%v); signalling without a head\n",
			branch, remote, headErr)
	}

	body, err := json.Marshal(signal{Branch: branch, Head: head})
	if err != nil {
		fmt.Fprintf(s.log, "completion signal: could not encode the payload: %v\n", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+donePath, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(s.log, "completion signal: could not build the request: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		fmt.Fprintf(s.log, "completion signal: not delivered (%v); the pushed branch remains the source of truth\n", err)
		return
	}
	defer resp.Body.Close()
	answer, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil {
		fmt.Fprintf(s.log, "completion signal: the door's answer was unreadable: %v\n", readErr)
		return
	}
	var outcome struct {
		Delivered bool   `json:"delivered"`
		Detail    string `json:"detail"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(answer, &outcome)
	// A refusal from the door is an answer, not an error to propagate: the
	// four refusal verdicts (unknown, revoked, orphaned, finished) are facts
	// about the run, and the run's own state already accounts for all of them.
	switch {
	case resp.StatusCode == http.StatusAccepted && outcome.Delivered:
		fmt.Fprintf(s.log, "completion signal: delivered (%s)\n", outcome.Detail)
	case resp.StatusCode == http.StatusAccepted:
		fmt.Fprintf(s.log, "completion signal: the door took the POST but the event did not land (%s)\n", outcome.Detail)
	default:
		detail := outcome.Detail
		if detail == "" {
			detail = outcome.Error
		}
		if detail == "" {
			detail = string(answer)
		}
		fmt.Fprintf(s.log, "completion signal: the door answered %d (%s); the pushed branch remains the source of truth\n",
			resp.StatusCode, detail)
	}
}

// remoteHead reads where a branch stands on the remote — the pushed head, not
// the local one, because "the branch is the source of truth" names the ref
// everybody else can read. git's own plumbing answers; no network beyond the
// remote git already talks to.
func remoteHead(ctx context.Context, repo, remote, branch string) (string, error) {
	if repo == "" {
		repo = "."
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "ls-remote", remote, "refs/heads/"+branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && shaPattern.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("refs/heads/%s is not on %s", branch, remote)
}
