package runstate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The epic-6in stall, reproduced: an https remote that accepts the connection
// and then says nothing. On 2026-09-28 the orchestrator sat thirty minutes in
// its checkpoint push with `git remote-https origin` alive and silent, because
// TransportEnv bounded ssh and nothing bounded curl — whose defaults wait on
// an established, silent connection forever — and when a person killed the
// helper by pid the run died on an error nobody had classified.
//
// Nothing here is a model: the store's own runner pushes over git's real
// smart-http transport to a real (if unhelpful) HTTP server. The server
// answers nothing; it holds every request open until the test ends.
//
// The bound under test is the production one, reached through the path an
// operator uses: GIT_HTTP_LOW_SPEED_TIME is shortened in the environment so
// the test waits seconds rather than a minute, and GIT_HTTP_LOW_SPEED_LIMIT is
// left for TransportEnv to supply. Without TransportEnv's limit curl has no
// low-speed abort at all, and the push below hangs until the watchdog.
//
// short: one push against a local server that aborts after ~2s; no harness
func TestAPushToASilentHTTPSRemoteGivesUpInsideTheBoundAndIsTransient(t *testing.T) {
	release := make(chan struct{})
	requests := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.Method + " " + r.URL.String():
		default:
		}
		// Accept, then go silent: the socket is live, the headers never come.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	// Cleanups run last-registered first: the handlers are released before
	// the server's Close waits for them.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	t.Setenv("GIT_HTTP_LOW_SPEED_TIME", "2")
	t.Setenv("GIT_HTTP_LOW_SPEED_LIMIT", "") // unset for git's purposes; TransportEnv supplies the production limit
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	repo := t.TempDir()
	gitRun(t, repo, "init", "--quiet", ".")
	g := newGit(repo, "ticfac test", "ticfac@example.com", RemoteRetry{Attempts: 1})
	head, err := g.run("commit-tree", emptyTree(t, g), "-m", "checkpoint")
	if err != nil {
		t.Fatal(err)
	}

	const watchdog = 45 * time.Second
	type result struct {
		stderr string
		err    error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		_, stderr, err := g.try(nil, nil, "push", "--force-with-lease=refs/heads/epic/6in:"+head,
			server.URL+"/pengelbrecht/ticfac.git", head+":refs/heads/epic/6in")
		done <- result{stderr, err}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(watchdog):
		t.Fatalf("the push to a silent https remote was still waiting after %s (request seen: %q): "+
			"nothing bounds the https transport, which is the epic-6in stall", watchdog, drain(requests))
	}
	elapsed := time.Since(started)

	if got.err == nil {
		t.Fatal("the push to a remote that never answered succeeded")
	}
	t.Logf("gave up after %s with: %s", elapsed.Round(time.Millisecond), got.stderr)
	if elapsed > 20*time.Second {
		t.Errorf("the push took %s to give up; the bound under test is 2s of silence", elapsed)
	}
	if drain(requests) == "" {
		t.Error("the server never saw a request: the push did not reach the silent remote, so this proves nothing")
	}
	if class := ClassifyRemote(got.err); class != RemoteTransient {
		t.Errorf("a push that timed out on a silent https remote classified as %v, want RemoteTransient: "+
			"the retry and the supervisor's remote_transient resume would not answer it, and a person would\n%v",
			class, got.err)
	}
}

func emptyTree(t *testing.T, g *git) string {
	t.Helper()
	tree, err := g.runInput([]byte{}, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func drain(requests chan string) string {
	var seen []string
	for {
		select {
		case r := <-requests:
			seen = append(seen, r)
		default:
			return strings.Join(seen, ", ")
		}
	}
}
