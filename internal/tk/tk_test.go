package tk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type recordingRunner struct {
	requests []Request
	results  []Result
}

func (r *recordingRunner) Run(_ context.Context, request Request) Result {
	r.requests = append(r.requests, request)
	if len(r.results) == 0 {
		return Result{ExitCode: 1}
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result
}

func TestNewChecksVersionOnceAndPinsEveryCall(t *testing.T) {
	runner := &recordingRunner{results: []Result{
		{Stdout: []byte(`{"tk":"dev","contract":1,"supported_contracts":[1],"min_tk_version":"0.32.0","manifest":"contracts/tk-json-manifest.json"}`)},
		{Stdout: []byte(`{"id":"a1","title":"fixture","status":"open","priority":1,"type":"task","owner":"","created_by":"fixture","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}`)},
	}}

	client, err := New(Options{
		Runner: runner,
		Dir:    t.TempDir(),
		Env:    map[string]string{ContractEnv: "99", "TK_FIXTURE": "yes"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Show(context.Background(), "a1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("runner saw %d calls, want startup version plus show", len(runner.requests))
	}
	if got := runner.requests[0].Args; len(got) != 2 || got[0] != "version" || got[1] != "--json" {
		t.Errorf("startup args = %v, want [version --json]", got)
	}
	if got := runner.requests[1].Args; len(got) != 3 || got[0] != "show" || got[1] != "a1" || got[2] != "--json" {
		t.Errorf("show args = %v, want [show a1 --json]", got)
	}
	for i, request := range runner.requests {
		if got := request.Env[contractEnv]; got != contractEnvValue {
			t.Errorf("request %d %s = %q, want %q", i, contractEnv, got, contractEnvValue)
		}
		if got := request.Env["TK_FIXTURE"]; got != "yes" {
			t.Errorf("request %d lost custom environment: %v", i, request.Env)
		}
	}
}

func TestEveryManifestCommandIsExercisedAndValidated(t *testing.T) {
	const tick = `{"id":"a1","title":"fixture","status":"open","priority":1,"type":"task","owner":"","created_by":"fixture","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}`
	runner := &recordingRunner{results: []Result{
		{Stdout: []byte(versionJSON())},
		{Stdout: []byte(tick)},
		{Stdout: []byte(`{"ticks":[` + tick + `],"filters":{"title_contains":"fixture"}}`)},
		{Stdout: []byte(`{"ticks":[` + tick + `]}`)},
		{Stdout: []byte(`{` + strings.TrimPrefix(tick, "{")[:len(strings.TrimPrefix(tick, "{"))-1] + `,"action":"implement"}`)},
		{Stdout: []byte(`{"blocked_by":["d1"],"blocks":[` + tick + `]}`)},
		{Stdout: []byte(graphJSON())},
		{Stdout: []byte(`{"changes":[".tick/issues/a1.json"]}`)},
		{Stdout: []byte(tick)},
		{Stdout: []byte(tick)},
		{Stdout: []byte(tick)},
		{Stdout: []byte(tick)},
		{Stdout: []byte(tick)},
		{ExitCode: 0},
		{ExitCode: 0},
	}}
	client, err := New(Options{Runner: runner, Dir: "/fixture"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := client.Show(ctx, "a1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if _, err := client.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := client.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	next, err := client.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next == nil || next.Action != "implement" {
		t.Fatalf("Next = %#v, want an implement tick", next)
	}
	if _, err := client.Deps(ctx, "a1"); err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if _, err := client.Graph(ctx, "e1"); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if _, err := client.Claim(ctx, "a1", "worker"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := client.Update(ctx, "a1", "updated"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := client.Note(ctx, "a1", "noted"); err != nil {
		t.Fatalf("Note: %v", err)
	}
	if _, err := client.Close(ctx, "a1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := client.Reopen(ctx, "a1"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := client.MergeFile(ctx, "base", "ours", "theirs", "result"); err != nil {
		t.Fatalf("MergeFile: %v", err)
	}
	if err := client.MergeActivity(ctx, "ancestor", "current", "other", "result"); err != nil {
		t.Fatalf("MergeActivity: %v", err)
	}

	want := [][]string{
		{"version", "--json"},
		{"show", "a1", "--json"},
		{"list", "--all", "--json"},
		{"ready", "--all", "--json"},
		{"next", "--all", "--json"},
		{"deps", "a1", "--json"},
		{"graph", "e1", "--json"},
		{"status", "--json"},
		{"update", "a1", "--status", "in_progress", "--owner", "worker", "--json"},
		{"update", "a1", "--notes", "updated", "--json"},
		{"note", "a1", "noted", "--json"},
		{"close", "a1", "--json"},
		{"reopen", "a1", "--json"},
		{"merge-file", "base", "ours", "theirs", "result"},
		{"merge-activity", "ancestor", "current", "other", "result"},
	}
	if len(runner.requests) != len(want) {
		t.Fatalf("runner saw %d requests, want %d", len(runner.requests), len(want))
	}
	for i, request := range runner.requests {
		if got, expected := request.Args, want[i]; fmt.Sprint(got) != fmt.Sprint(expected) {
			t.Errorf("request %d args = %v, want %v", i, got, expected)
		}
		if request.Env[ContractEnv] != ContractEnvValue {
			t.Errorf("request %d did not pin %s=%s: %v", i, ContractEnv, ContractEnvValue, request.Env)
		}
	}
}

func TestResponseMustSatisfyManifestSchemaBeforeDecoding(t *testing.T) {
	runner := &recordingRunner{results: []Result{
		{Stdout: []byte(versionJSON())},
		{Stdout: []byte(`{"id":"a1"}`)},
	}}
	client, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := client.Show(context.Background(), "a1")
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("Show error = %v, want *ResponseError", err)
	}
	if got.ID != "" {
		t.Fatalf("Show returned a decoded value after schema refusal: %#v", got)
	}
	if !strings.Contains(responseErr.Error(), `missing required property "title"`) {
		t.Errorf("schema error = %v, want a required-property refusal", responseErr)
	}
}

func TestUnsupportedContractIsTypedAtStartupAndOnEveryCommand(t *testing.T) {
	t.Run("startup", func(t *testing.T) {
		runner := &recordingRunner{results: []Result{{ExitCode: ExitUnsupportedContract, Stderr: []byte("requested 1; serves 2")}}}
		_, err := New(Options{Runner: runner})
		var unsupported *ErrUnsupportedContract
		if !errors.As(err, &unsupported) {
			t.Fatalf("New error = %v, want *ErrUnsupportedContract", err)
		}
		if unsupported.ExitCode != ExitUnsupportedContract {
			t.Errorf("ExitCode = %d, want %d", unsupported.ExitCode, ExitUnsupportedContract)
		}
	})

	t.Run("command", func(t *testing.T) {
		runner := &recordingRunner{results: []Result{
			{Stdout: []byte(versionJSON())},
			{ExitCode: ExitUnsupportedContract, Stderr: []byte("requested 1; serves 2")},
		}}
		client, err := New(Options{Runner: runner})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = client.Show(context.Background(), "a1")
		var unsupported *ErrUnsupportedContract
		if !errors.As(err, &unsupported) {
			t.Fatalf("Show error = %v, want *ErrUnsupportedContract", err)
		}
		if unsupported.Command != "show" {
			t.Errorf("Command = %q, want show", unsupported.Command)
		}
	})
}

func TestDispatchWidthIsTyped(t *testing.T) {
	runner := &recordingRunner{results: []Result{
		{Stdout: []byte(versionJSON())},
		{ExitCode: ExitDispatchWidth, Stderr: []byte("dispatch width exceeded")},
		{ExitCode: ExitDispatchWidth, Stderr: []byte("dispatch width exceeded")},
	}}
	client, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Claim(context.Background(), "a1", "worker")
	var dispatch *ErrDispatchWidth
	if !errors.As(err, &dispatch) {
		t.Fatalf("Claim error = %v, want *ErrDispatchWidth", err)
	}
	if dispatch.ExitCode != ExitDispatchWidth {
		t.Errorf("ExitCode = %d, want %d", dispatch.ExitCode, ExitDispatchWidth)
	}
}

func TestMinimumTkVersionIsCheckedOnceAtStartup(t *testing.T) {
	runner := &recordingRunner{results: []Result{{Stdout: []byte(versionJSONWithTk("0.31.9"))}}}
	_, err := New(Options{Runner: runner})
	var minimum *ErrMinimumVersion
	if !errors.As(err, &minimum) {
		t.Fatalf("New error = %v, want *ErrMinimumVersion", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner saw %d requests after startup refusal, want 1", len(runner.requests))
	}
}

func versionJSON() string { return versionJSONWithTk("dev") }

func versionJSONWithTk(version string) string {
	return fmt.Sprintf(`{"tk":%q,"contract":1,"supported_contracts":[1],"min_tk_version":"0.32.0","manifest":"contracts/tk-json-manifest.json"}`, version)
}

func graphJSON() string {
	return `{"epic":{"id":"e1","title":"epic"},"needs_planning":false,"missing_process_ticks":[],"unjustified_gates":[],"stats":{"total_tasks":1,"wave_count":1,"max_parallel":1,"ready_for_agent":1,"awaiting_human":0,"deferred":0},"dispatch":{"max_parallel":0,"in_flight":0,"in_flight_ids":[],"free":-1,"now":[]},"waves":[{"wave":1,"parallel":1,"ready":true,"tasks":[{"id":"a1","title":"fixture","priority":1,"status":"open","agent_ready":true}]}],"critical_path":1}`
}
