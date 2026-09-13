package cli

// The run-id resolution tests, ported from ticks'
// cmd/tk/cmd/cloud_runid_test.go: a truncated run id is resolved against the
// factory's run index before any run-scoped read is made, and the resolution
// is reported on stderr so a --json read stays parseable — which here reads
// the two streams straight off Run rather than cobra's captured pair.

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// A gateway an operator can read with no factory configured at all: trace is a
// gateway command, and the factory is only reachable for resolving a prefix.
func configureTraceGatewayWithoutFactory(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config, err := credentials.LoadFrom(filepath.Join(home, credentials.FileName))
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	config.Set(credentials.KeyGatewayURL, "https://gateway.ai.cloudflare.com/v1/acct-id/ticks-gw")
	config.Set(credentials.KeyCloudflareAPIToken, "cf-test-token")
	if err := config.Save(); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
}

// The run id in the tick that found this: a full one, and the head an operator
// actually has to hand after a wrapped terminal line or a truncating copy.
const (
	fullTestRunID  = "run_62c289d1e57942cea5fef6c1a508a0fd"
	shortTestRunID = "run_62c289d1"
	siblingRunID   = "run_62c289d1aaaa4aaabaaacaaadaaaeaaa"
)

// A run index answer with the given run ids, shaped like the factory's.
func cloudRunIndex(ids ...string) map[string]any {
	runs := make([]any, 0, len(ids))
	for _, id := range ids {
		runs = append(runs, map[string]any{
			"run_id": id, "state": "completed", "epic": "1vn", "project": "acme/project",
		})
	}
	return map[string]any{"runs": runs}
}

// A run id is "run_" plus 32 hex characters. That shape is the whole basis for
// telling a prefix from an id, so it is pinned rather than assumed.
func TestCloudRunIDPrefixRecognition(t *testing.T) {
	for _, testCase := range []struct {
		id   string
		want bool
	}{
		{shortTestRunID, true},
		{"run_6", true},
		{fullTestRunID, false},        // a whole id is not a prefix of one
		{"run_live", false},           // not hex: cannot be the head of an id
		{"run_", false},               // no head at all
		{"", false},                   // ditto
		{"62c289d1", false},           // not a run id at all
		{fullTestRunID + "aa", false}, // longer than an id
	} {
		if got := isCloudRunIDPrefix(testCase.id); got != testCase.want {
			t.Errorf("isCloudRunIDPrefix(%q) = %v, want %v", testCase.id, got, testCase.want)
		}
	}
}

// THE bug (tick c5i): `tk cloud trace run_62c289d1` answered "No AI Gateway
// calls are stamped with run run_62c289d1" — true of the prefix, false of the
// run, and read by the operator as "this run has no telemetry". The prefix is
// resolved against the runs the factory knows about before the gateway is
// asked anything.
func TestCloudTraceResolvesAShortRunIDAgainstTheFactory(t *testing.T) {
	configureTraceGateway(t)
	_, factory := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method == http.MethodGet && request.Path == "/api/runs" {
			return http.StatusOK, cloudRunIndex(fullTestRunID, "run_"+strings.Repeat("b", 32))
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	gateway := traceRunGateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", shortTestRunID})
	if code != exitSuccess {
		t.Fatalf("cloud trace %s: %s\n%s", shortTestRunID, stderr.String(), out.String())
	}
	output := out.String()

	if strings.Contains(output, "No AI Gateway calls") {
		t.Fatalf("a prefix was answered with a confident negative:\n%s", output)
	}
	if !strings.Contains(output, "Dispatching wave 1.") {
		t.Errorf("the resolved run's conversation was not read:\n%s", output)
	}
	// Said, not done silently: the operator asked about one string and was
	// answered about another.
	if !strings.Contains(output, shortTestRunID) || !strings.Contains(output, fullTestRunID) {
		t.Errorf("the resolution is not reported:\n%s", output)
	}
	// The gateway is filtered on the FULL id — the prefix never reaches it.
	if len(*gateway) == 0 {
		t.Fatal("the gateway was never read")
	}
	filters := (*gateway)[0].Query.Get("filters")
	if !strings.Contains(filters, fullTestRunID) {
		t.Errorf("the gateway filter does not carry the resolved id: %s", filters)
	}
	if len(*factory) != 1 || (*factory)[0].Query.Get("limit") == "" {
		t.Errorf("the run index was not read with a widened window: %#v", *factory)
	}
}

// A prefix that names more than one run is refused with both ids, never
// resolved to whichever came back first.
func TestCloudTraceRefusesAnAmbiguousRunIDPrefix(t *testing.T) {
	configureTraceGateway(t)
	newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusOK, cloudRunIndex(fullTestRunID, siblingRunID)
	})
	gateway := traceRunGateway(t)
	code, _, stderr := runCloudArgs(t, []string{"cloud", "trace", shortTestRunID})
	if code == exitSuccess {
		t.Fatal("an ambiguous prefix was resolved instead of refused")
	}
	for _, want := range []string{fullTestRunID, siblingRunID} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %s: %s", want, stderr.String())
		}
	}
	if len(*gateway) != 0 {
		t.Errorf("an ambiguous prefix still reached the gateway: %#v", *gateway)
	}
}

// The window the index answers with is bounded, so "no match" is a statement
// about that window and never about the run. It must not read as one.
func TestCloudTraceRefusesAnUnmatchedPrefixWithoutADenial(t *testing.T) {
	configureTraceGateway(t)
	newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusOK, cloudRunIndex("run_" + strings.Repeat("c", 32))
	})
	gateway := traceRunGateway(t)
	code, _, stderr := runCloudArgs(t, []string{"cloud", "trace", shortTestRunID})
	if code == exitSuccess {
		t.Fatal("an unresolvable prefix was not refused")
	}
	if !strings.Contains(stderr.String(), "prefix") && !strings.Contains(stderr.String(), "head of a run id") {
		t.Errorf("the refusal does not say the argument is a prefix: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "index") {
		t.Errorf("the refusal does not say what was searched: %s", stderr.String())
	}
	if len(*gateway) != 0 {
		t.Errorf("an unresolved prefix still reached the gateway: %#v", *gateway)
	}
}

// A whole run id costs no lookup: the fast path must not acquire a dependency
// on the factory for a command that reads the gateway.
func TestCloudTraceDoesNotConsultTheFactoryForAFullRunID(t *testing.T) {
	configureTraceGateway(t)
	_, factory := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusInternalServerError, map[string]any{"error": "the index must not be read"}
	})
	traceRunGateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", fullTestRunID})
	if code != exitSuccess {
		t.Fatalf("cloud trace %s: %s\n%s", fullTestRunID, stderr.String(), out.String())
	}
	if len(*factory) != 0 {
		t.Errorf("a full run id was still resolved against the factory: %#v", *factory)
	}
}

// The resolution note is a diagnostic, not data: --json has to stay parseable.
func TestCloudTraceResolutionNoteStaysOutOfTheJSON(t *testing.T) {
	configureTraceGateway(t)
	newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusOK, cloudRunIndex(fullTestRunID)
	})
	traceRunGateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", shortTestRunID, "--json"})
	if code != exitSuccess {
		t.Fatalf("cloud trace --json: %s", stderr.String())
	}
	if strings.Contains(out.String(), "resolved") {
		t.Errorf("the resolution note was written to stdout, where it corrupts --json:\n%s", out.String())
	}
	if !strings.Contains(stderr.String(), fullTestRunID) {
		t.Errorf("the resolution was not reported on stderr:\n%s", stderr.String())
	}
	if !strings.Contains(out.String(), `"run_id": "`+fullTestRunID+`"`) {
		t.Errorf("--json does not report the resolved run id:\n%s", out.String())
	}
}

// The same trap on the other two run-scoped reads: `tk cloud logs` answers a
// prefix with the factory's 404, and `tk cloud status` with the same.
func TestCloudLogsResolvesAShortRunID(t *testing.T) {
	setupCloudRepo(t, false)
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return http.StatusOK, cloudRunIndex(fullTestRunID)
		case "/api/runs/" + fullTestRunID + "/logs":
			return http.StatusOK, map[string]any{
				"run_id": fullTestRunID, "state": "completed",
				"text": "reconciling epic 1vn\n", "bytes": 21, "total_bytes": 21,
			}
		}
		return http.StatusNotFound, map[string]any{"error": "unknown_run"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "logs", shortTestRunID})
	if code != exitSuccess {
		t.Fatalf("cloud logs %s: %s\n%s", shortTestRunID, stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "reconciling epic 1vn") {
		t.Fatalf("the resolved run's log was not printed:\n%s", out.String())
	}
	if len(*requests) != 2 {
		t.Errorf("logs made %d factory requests, want the index read plus the log read: %#v", len(*requests), *requests)
	}
}

func TestCloudStatusResolvesAShortRunID(t *testing.T) {
	setupCloudRepo(t, false)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return http.StatusOK, cloudRunIndex(fullTestRunID)
		case "/api/runs/" + fullTestRunID:
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": fullTestRunID, "state": "completed", "epic": "1vn"},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "unknown_run"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", shortTestRunID})
	if code != exitSuccess {
		t.Fatalf("cloud status %s: %s\n%s", shortTestRunID, stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "Cloud run "+fullTestRunID) {
		t.Fatalf("status did not report the resolved run:\n%s", out.String())
	}
}

// A prefix resolved from the lease or the queue is still resolved: those runs
// are exactly the ones an operator is watching, and a live run is the most
// likely thing to have a half-copied id.
func TestCloudStatusResolvesAPrefixHeldByTheLease(t *testing.T) {
	setupCloudRepo(t, false)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return http.StatusOK, map[string]any{
				"runs": []any{},
				"projects": []any{map[string]any{
					"project": "acme/project",
					"lease":   map[string]any{"run_id": fullTestRunID, "epic": "1vn"},
					"queued":  []any{map[string]any{"run_id": siblingRunID, "blocked_by": fullTestRunID}},
				}},
			}
		case "/api/runs/" + fullTestRunID:
			return http.StatusOK, map[string]any{
				"run": map[string]any{"run_id": fullTestRunID, "state": "running", "epic": "1vn"},
			}
		}
		return http.StatusNotFound, map[string]any{"error": "unknown_run"}
	})
	configureCloudFactory(t, endpoint)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "status", "run_62c289d1e"})
	if code != exitSuccess {
		t.Fatalf("cloud status: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "state: running") {
		t.Fatalf("the lease holder was not resolved from the index:\n%s", out.String())
	}
}

// `tk cloud trace` reads the operator's gateway, not the factory — but only
// the factory can name its runs. Without one, a prefix is refused for what it
// is instead of being passed through to a negative.
func TestCloudTraceWithoutAFactoryRefusesAPrefixPlainly(t *testing.T) {
	configureTraceGatewayWithoutFactory(t)
	code, _, stderr := runCloudArgs(t, []string{"cloud", "trace", shortTestRunID})
	if code == exitSuccess {
		t.Fatal("a prefix with no factory to resolve it against was not refused")
	}
	if !strings.Contains(stderr.String(), shortTestRunID) {
		t.Errorf("the refusal does not name the argument: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "factory") {
		t.Errorf("the refusal does not say what is missing: %s", stderr.String())
	}
}
