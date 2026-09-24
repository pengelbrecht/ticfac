package runsignal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The completion signal's contract with the door (tick 7eq), at this end: what
// a finished orchestrator posts, when it posts nothing, and the rule the whole
// design rests on — the POST is best effort and NEVER a verdict, so no outcome
// of sending it reaches the run's own exit path.

// gitIn runs git in a directory, failing the test on the first refusal.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// repoWithRemoteBranch builds a clone with a bare origin and one pushed branch,
// returning the clone (where run-epic would work) and the branch's pushed head.
func repoWithRemoteBranch(t *testing.T, branch string) (repo, head string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	gitIn(t, root, "init", "--quiet", "--bare", bare)
	repo = filepath.Join(root, "clone")
	gitIn(t, root, "clone", "--quiet", bare, repo)
	gitIn(t, repo, "config", "user.email", "signal@example.com")
	gitIn(t, repo, "config", "user.name", "signal test")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "--quiet", "-m", "the run's work")
	gitIn(t, repo, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	head = strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	return repo, head
}

// door is the factory's done door as an httptest server: it records what the
// signaller posted and answers what the test tells it to.
type door struct {
	mu    sync.Mutex
	calls []struct {
		authorization string
		path          string
		body          signal
	}
	answer    func(w http.ResponseWriter)
	serverURL string
}

func newDoor(t *testing.T, answer func(w http.ResponseWriter)) *door {
	t.Helper()
	d := &door{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the door could not read the body: %v", err)
		}
		var posted signal
		_ = json.Unmarshal(body, &posted)
		d.mu.Lock()
		d.calls = append(d.calls, struct {
			authorization string
			path          string
			body          signal
		}{r.Header.Get("Authorization"), r.URL.Path, posted})
		d.mu.Unlock()
		d.answer(w)
	}))
	t.Cleanup(server.Close)
	d.serverURL = server.URL
	return d
}

// The URL field lives on the struct under test via New; tests read it back
// through a tiny indirection so the fake never holds a second copy of it.
func (d *door) url() string { return d.serverURL }
func TestDonePostsTheBranchAndItsPushedHead(t *testing.T) {
	t.Parallel()

	repo, head := repoWithRemoteBranch(t, "epic/ko8")
	d := newDoor(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"delivered":true,"detail":"signalled"}`))
	})
	log := &strings.Builder{}

	New(d.url(), "tkr_run_token", log).Done(context.Background(), repo, "origin", "epic/ko8")

	if len(d.calls) != 1 {
		t.Fatalf("the door was called %d times, want 1", len(d.calls))
	}
	call := d.calls[0]
	if call.authorization != "Bearer tkr_run_token" {
		t.Errorf("authorization %q, want the run's own gateway token", call.authorization)
	}
	if call.path != "/api/done" {
		t.Errorf("path %q, want /api/done", call.path)
	}
	if call.body.Branch != "epic/ko8" {
		t.Errorf("branch %q, want epic/ko8", call.body.Branch)
	}
	// The pushed head, not the local one: "the branch is the source of truth"
	// names the ref the durable layer holds.
	if call.body.Head != head {
		t.Errorf("head %q, want the pushed head %q", call.body.Head, head)
	}
	if !strings.Contains(log.String(), "delivered") {
		t.Errorf("a delivered signal is said to the log, got: %s", log.String())
	}
}

func TestDoneStillSignalsWhenTheBranchNeverLanded(t *testing.T) {
	t.Parallel()

	repo, _ := repoWithRemoteBranch(t, "epic/ko8")
	d := newDoor(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"delivered":true,"detail":"signalled"}`))
	})
	log := &strings.Builder{}

	// A run that failed before pushing anything: the remote has no such branch,
	// so there is no head to resolve — and the wake-up is still worth sending.
	New(d.url(), "tkr_run_token", log).Done(context.Background(), repo, "origin", "epic/never-pushed")

	if len(d.calls) != 1 {
		t.Fatalf("the door was called %d times, want 1 — a headless signal still wakes the supervisor", len(d.calls))
	}
	if d.calls[0].body.Branch != "epic/never-pushed" {
		t.Errorf("branch %q, want epic/never-pushed", d.calls[0].body.Branch)
	}
	if d.calls[0].body.Head != "" {
		t.Errorf("head %q, want none — nothing was pushed", d.calls[0].body.Head)
	}
	if !strings.Contains(log.String(), "without a head") {
		t.Errorf("an unresolvable head is said to the log, got: %s", log.String())
	}
}

func TestDoneSwallowsEveryDeliveryFailure(t *testing.T) {
	t.Parallel()

	repo, _ := repoWithRemoteBranch(t, "epic/ko8")
	log := &strings.Builder{}

	// A door that refuses — the four refusal verdicts are facts about the run,
	// not errors for a finished container to act on.
	refusing := newDoor(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"run_token_revoked","detail":"the run was stopped"}`))
	})
	New(refusing.url(), "tkr_run_token", log).Done(context.Background(), repo, "origin", "epic/ko8")
	if !strings.Contains(log.String(), "the pushed branch remains the source of truth") {
		t.Errorf("a refused signal names the branch as the truth, got: %s", log.String())
	}

	// A door that is not there at all — the container dying after finishing and
	// before its callback lands is the exact case the design says the branch
	// must cover, so this is the one failure the whole rule is about.
	log.Reset()
	New("http://127.0.0.1:1", "tkr_run_token", log).Done(context.Background(), repo, "origin", "epic/ko8")
	if !strings.Contains(log.String(), "not delivered") {
		t.Errorf("an undeliverable signal is said and swallowed, got: %s", log.String())
	}

	// And no path through Done may panic on a nil Signaller: FromEnv returns
	// nil for a local run, and the call site does not branch on that.
	FromEnv(&strings.Builder{}).Done(context.Background(), repo, "origin", "epic/ko8")
}

func TestFromEnvReadsTheContainerConfiguration(t *testing.T) {
	t.Setenv("TICKS_FACTORY_URL", "https://factory.example.com/")
	t.Setenv("TICKS_FACTORY_TOKEN", "tkr_run_token")
	log := &strings.Builder{}
	s := FromEnv(log)
	if s == nil {
		t.Fatal("a container holding the factory configuration gets a signaller")
	}
	// The trailing slash is trimmed: the door's path is appended, not joined.
	if s.url != "https://factory.example.com" {
		t.Errorf("url %q, want the trailing slash trimmed", s.url)
	}

	t.Setenv("TICKS_FACTORY_URL", "")
	if s := FromEnv(log); s != nil {
		t.Error("a run with no factory URL is a local run: no signaller, and the no-op is the design")
	}
}
