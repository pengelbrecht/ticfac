package sandboximage

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// feedNotes returns every lifecycle line the entrypoint relayed to the
// factory's feed-relay door, in the order it relayed them — decoded from the
// POST bodies the curl stub recorded.
func (f *fixture) feedNotes() []string {
	f.t.Helper()
	var details []string
	var inFeedPost bool
	for _, line := range strings.Split(f.probeCalls(), "\n") {
		if strings.HasPrefix(line, "URL=") {
			inFeedPost = strings.HasSuffix(line, "/api/feed")
			continue
		}
		if !inFeedPost || !strings.HasPrefix(line, `ARG={"stream"`) {
			continue
		}
		var body struct {
			Stream string `json:"stream"`
			Offset int64  `json:"offset"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "ARG=")), &body); err != nil {
			f.t.Fatalf("an unreadable feed-relay body %q: %v", line, err)
		}
		if body.Stream != "boot" {
			f.t.Errorf("the entrypoint relayed stream %q, want boot", body.Stream)
		}
		for _, raw := range strings.Split(strings.TrimSuffix(body.Text, "\n"), "\n") {
			var event struct {
				SchemaVersion int     `json:"schema_version"`
				RunID         string  `json:"run_id"`
				TickID        *string `json:"tick_id"`
				Attempt       *int    `json:"attempt"`
				Stage         string  `json:"stage"`
				Detail        string  `json:"detail"`
			}
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				f.t.Fatalf("an unreadable lifecycle line %q: %v", raw, err)
			}
			if event.SchemaVersion != 1 || event.RunID != "run_test" || event.Stage != "container" {
				f.t.Errorf("a lifecycle line that is not the run's own run-level line: %s", raw)
			}
			details = append(details, event.Detail)
		}
	}
	return details
}

func (f *fixture) withFactory() {
	f.env[EnvFactoryURL] = "https://factory.example.com"
	f.env[EnvFactoryToken] = "tkr_boot_token"
	f.env["TICKS_FEED_NOTES"] = filepath.Join(f.root, "feed-notes.jsonl")
}

// A cloud boot used to be silent on the run's feed from "run started" to "the
// orchestrator exited": the clone, the probes and the pre-flight said nothing
// an operator following the run could read. The boot now says where it is.
func TestEntrypointRelaysItsLifecycleToTheRunFeed(t *testing.T) {
	f := newFixture(t, "- `true` — nothing to check\n")
	f.withFactory()
	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	notes := strings.Join(f.feedNotes(), "\n")
	mustContain(t, notes, "booting: phase run, epic ko8", "the boot announces itself")
	mustContain(t, notes, "probes green", "the probes' verdict is on the feed")
	mustContain(t, notes, "pre-flight green: starting the orchestrator", "the hand-off is on the feed")
	if strings.Contains(notes, "exited") {
		t.Errorf("a boot that reached its orchestrator reported an exit:\n%s", notes)
	}
	mustContain(t, f.probeCalls(), "ARG=Authorization: Bearer tkr_boot_token", "the relay rides the run's own token")
}

// A boot that dies before its orchestrator starts leaves a line saying so —
// with its exit code and the step it died in.
func TestEntrypointRelaysTheExitOfABootThatDiedEarly(t *testing.T) {
	f := newFixture(t, "")
	f.withFactory()
	f.addMigratedEnvironment("git identity", "false")
	out, code := f.run()
	if code != ExitPreflight {
		t.Fatalf("exit %d, want %d\n%s", code, ExitPreflight, out)
	}
	notes := strings.Join(f.feedNotes(), "\n")
	mustContain(t, notes, "exited 5 while running the pre-flight, before the orchestrator started",
		"the dead boot's exit is on the feed")
}

// No factory, no relay: a container without the bridge says nothing to one.
func TestEntrypointRelaysNothingWithoutAFactory(t *testing.T) {
	f := newFixture(t, "- `true`\n")
	if _, code := f.run(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if strings.Contains(f.probeCalls(), "/api/feed") {
		t.Errorf("a container with no factory posted to the feed relay:\n%s", f.probeCalls())
	}
}
