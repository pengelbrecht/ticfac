package cli

// The harness preflight of a cloud submission (epic 43y, tick kkt): the
// acceptance criteria, as tests against a fake factory and a real pushed
// checkout. [A4]'s cloud half cannot be run before the close-out merge (the
// factory deploys main only, and main does not carry this epic) — what CAN
// land inside the epic is the guard that makes the post-merge runbook
// mechanically enforceable: `ticfac run <epic> --cloud` refuses a submission
// whose branch routes a cloud job to a harness kind the deployed factory's
// image does not ship, before anything is pushed or booted, naming the
// runbook's order — instead of the wave of dead containers ("unknown harness
// kind") tick twa found waiting for exactly that mistake.
//
// The fake factory is the same harness every cloud command test uses
// (newCloudFactory + configureCloudFactory + setupCloudRepo), and the
// runners-config fixtures are resolved by the real cloud profile set — the
// same questions reconcile.CheckRouting asks, so a fixture that stops
// resolving is a test that says so, never one that skips.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// preflightCommonRunners is a minimal valid .tick/runners.toml: every role a
// run dispatches, cloud-routable, on one kind the fixtures vary. It is the
// repository's own shape with the comments and the findings routing stripped.
const preflightCommonRunners = `version = 2

[orchestration]
substrate = "auto"

[roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[roles.review]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
`

// preflightOverlayOf is the cloud overlay with every kind replaced — the
// one knob each fixture turns.
func preflightOverlayOf(kind string) string {
	return fmt.Sprintf(`version = 2

[roles.implement]
kind = %q
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[roles.review]
kind = %q
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review.tiers.strong]
kind = %q
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = %q
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
`, kind, kind, kind, kind)
}

// commitPreflightConfig writes a runners config (and, when overlay is not
// empty, a cloud overlay naming it) at HEAD and pushes it — HEAD is the
// boundary the preflight reads, so the fixture commits before submitting.
func commitPreflightConfig(t *testing.T, repo string, common, overlay string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".tick"), 0o755); err != nil {
		t.Fatalf("mkdir .tick: %v", err)
	}
	if common != "" {
		if err := os.WriteFile(filepath.Join(repo, ".tick", "runners.toml"), []byte(common), 0o644); err != nil {
			t.Fatalf("write runners.toml: %v", err)
		}
	}
	if overlay != "" {
		if err := os.WriteFile(filepath.Join(repo, ".tick", "runners.cloud.toml"), []byte(overlay), 0o644); err != nil {
			t.Fatalf("write runners.cloud.toml: %v", err)
		}
	}
	execTestCmd(t, repo, "git", "add", ".tick")
	execTestCmd(t, repo, "git", "commit", "-m", "runners config")
	execTestCmd(t, repo, "git", "push", "origin", "main")
}

// deploymentAnswer is the factory's GET /api/deployment answer a test serves,
// with the kinds an image ships (nil = the field is absent: a factory that
// predates it).
func deploymentAnswer(version string, kinds []string) (int, any) {
	return deploymentAnswerWithSubscriptions(version, kinds, nil)
}

// deploymentAnswerWithSubscriptions is [deploymentAnswer] with the claude-sub
// subscription LABELS the deployment holds (tick tda): nil omits the field —
// a factory that predates it — and an empty-but-present slice is the honest
// "the rung is off" a config riding the rung is refused against.
func deploymentAnswerWithSubscriptions(version string, kinds, subs []string) (int, any) {
	body := map[string]any{
		"version":      version,
		"image_ref":    "registry/ticks-orchestrator@sha256:abc",
		"image_digest": "sha256:abc",
	}
	if kinds != nil {
		body["harness_kinds"] = kinds
	}
	if subs != nil {
		body["claude_sub_labels"] = subs
	}
	return 200, body
}

// requestSeen reports whether the recorded factory traffic holds one request
// of a method and path prefix.
func requestSeen(requests *[]cloudFactoryRequest, method, pathPrefix string) bool {
	for _, r := range *requests {
		if r.Method == method && strings.HasPrefix(r.Path, pathPrefix) {
			return true
		}
	}
	return false
}

// TestRunCloudRefusesAKindTheFactorysImageDoesNotShip: the factory answers
// with an image that does not ship the durable harness (omp and claude only —
// the image before jhp) while the branch routes its cloud jobs on it, spelled
// the way this repository's own overlay spells it ("pi", which resolution
// binds to pi-durable, tick twa). That is the state the runbook guards between
// the close-out merge and its deploy. The submission is refused locally,
// naming the job's RESOLVED kind (what its container would be told) and the
// runbook's factory-side fix, and the only traffic that reached the factory
// is the two reads.
func TestRunCloudRefusesAKindTheFactorysImageDoesNotShip(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightOverlayOf("pi"))
	started := cloudRunIDOf("bb22")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return deploymentAnswer("v1.0.0-7-gdeadbeef0123", []string{"omp", "claude"})
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			t.Errorf("the preflight refused nothing: the factory was asked to start %s", started)
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitGeneric {
		t.Fatalf("exit %d, want the failure class %d:\nstdout:\n%s\nstderr:\n%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	joined := stdout.String() + stderr.String()
	for _, want := range []string{
		"does not ship",
		"implement-tick: pi-durable",   // the offending job, named by the kind its container would be told
		"v1.0.0-7-gdeadbeef0123",       // which factory refused: the deployment to move
		"the fix is the factory's",     // no overlay cell can name a kind the old image ships
		`wait-deployed <merge sha>`,    // the runbook's step the message names
		"pi-durable-cloud-run-runbook", // the runbook, by the path it is at
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, joined)
		}
	}
	if len(rec.runIDs) != 0 {
		t.Errorf("the refused submission attached to a run: %v", rec.runIDs)
	}
}

// TestRunCloudSubmitsWhenEveryRoutedKindShips: the overlay routes the same
// jobs on the durable harness and the factory ships it — the post-merge end
// state the runbook walks to. Both spellings pass: "pi-durable", and "pi", a
// runner table's name for the same harness that this repository's own
// .tick/runners.cloud.toml carries and resolution binds to the hosted kind
// (tick twa) — refusing it would refuse this repository's every cloud run.
// The preflight passes silently and the submission goes through unchanged.
func TestRunCloudSubmitsWhenEveryRoutedKindShips(t *testing.T) {
	for _, tc := range []struct{ kind, run string }{
		{"pi-durable", "cc33"},
		{"pi", "cc34"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			stubCloudTk(t)
			repo, _, _ := setupCloudRepo(t, true)
			commitPreflightConfig(t, repo, preflightCommonRunners, preflightOverlayOf(tc.kind))
			started := cloudRunIDOf(tc.run)

			endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
				switch {
				case request.Method == http.MethodGet && request.Path == cloudIndexPath:
					if request.Query.Get("project") == "" {
						t.Errorf("the run index was read without the project window")
					}
					return 200, map[string]any{"runs": []any{}}
				case request.Method == http.MethodGet && request.Path == "/api/deployment":
					return deploymentAnswer("v1.0.0-8-gfeedface0123", []string{"omp", "claude", "pi-durable"})
				case request.Method == http.MethodPost && request.Path == cloudIndexPath:
					return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
				}
				t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
				return 404, map[string]any{"error": "not_found"}
			})
			configureCloudFactory(t, endpoint)
			rec := recordCloudAttach(t)

			code, stdout, stderr := runRunCloud(t, repo, "epic1")
			if code != exitSuccess {
				t.Fatalf("exit %d starting a cloud run the preflight passed:\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
			}
			if !requestSeen(requests, http.MethodPost, cloudIndexPath) {
				t.Errorf("the preflight passed but no run was submitted")
			}
			if len(rec.runIDs) != 1 || rec.runIDs[0] != started {
				t.Errorf("attached to %v, want [%s]", rec.runIDs, started)
			}
			if strings.Contains(stdout.String()+stderr.String(), "preflight") {
				t.Errorf("a passing preflight said something:\n%s%s", stdout.String(), stderr.String())
			}
		})
	}
}

// TestRunCloudPreflightWarnsWhenTheFactoryPredatesTheField: a factory whose
// /api/deployment predates harness_kinds (today's deployment until this epic
// merges) cannot be asked what it ships — the submission proceeds, with a
// warning that names the missing answer and how to wait for one that has it.
func TestRunCloudPreflightWarnsWhenTheFactoryPredatesTheField(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightOverlayOf("pi-durable"))
	started := cloudRunIDOf("dd44")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return deploymentAnswer("v0.9.3-42-gabcdef123456", nil)
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d: an old factory must not block submission:\nstderr:\n%s", code, stderr.String())
	}
	joined := stdout.String() + stderr.String()
	if !strings.Contains(joined, "does not report the harness kinds its image ships") ||
		!strings.Contains(joined, "wait-deployed") {
		t.Errorf("the warning does not name the missing answer and its fix:\n%s", joined)
	}
}

// TestRunCloudPreflightWarnsWhenTheDeploymentReadFails: a factory whose
// deployment read errors (here: the route is absent entirely) is the same
// honest "cannot ask" — proceed with a warning naming the error, never a
// refusal on transport.
func TestRunCloudPreflightWarnsWhenTheDeploymentReadFails(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightOverlayOf("pi-durable"))
	started := cloudRunIDOf("ee55")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return 404, map[string]any{"error": "not_found"}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d: an unreadable deployment must not block submission:\nstderr:\n%s", code, stderr.String())
	}
	joined := stdout.String() + stderr.String()
	if !strings.Contains(joined, "proceeding without it") {
		t.Errorf("the warning does not say it proceeded without the preflight:\n%s", joined)
	}
}

// TestRunCloudPreflightSkipsARepoWithNoRunnersConfig: a repository that
// declares no .tick/runners.toml routes its cloud containers on the
// factory's defaults, so the preflight has no branch half to check and must
// not even ask the factory what it ships.
func TestRunCloudPreflightSkipsARepoWithNoRunnersConfig(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	started := cloudRunIDOf("ff66")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d: a configless repo must submit as it always did:\nstderr:\n%s", code, stderr.String())
	}
	if requestSeen(requests, http.MethodGet, "/api/deployment") {
		t.Errorf("the preflight asked the factory about a repo whose routing never comes from the repo")
	}
	if strings.Contains(stdout.String()+stderr.String(), "preflight") {
		t.Errorf("the preflight said something about a repo it does not govern:\n%s%s", stdout.String(), stderr.String())
	}
}

// TestRunCloudRefusesARoutingThatDoesNotResolve: a branch whose cloud routing
// cannot resolve (here: a runners.toml with no cloud overlay, so every role is
// unrouted under the cloud substrate) is refused at submit — the same refusal
// a run makes at start, at the moment it still costs nothing. The message
// carries the underlying error, which names the cell to declare.
func TestRunCloudRefusesARoutingThatDoesNotResolve(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, "")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			t.Errorf("an unresolvable routing was submitted anyway")
			return 201, map[string]any{"run": map[string]any{"run_id": cloudRunIDOf("0077"), "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitGeneric {
		t.Fatalf("exit %d, want the failure class %d:\nstderr:\n%s", code, exitGeneric, stderr.String())
	}
	joined := stdout.String() + stderr.String()
	if !strings.Contains(joined, "the cloud routing at HEAD does not resolve") {
		t.Errorf("the refusal does not name the routing defect:\n%s", joined)
	}
	if !strings.Contains(joined, "cloud routing declared") {
		t.Errorf("the refusal does not carry the resolution error that names the cell:\n%s", joined)
	}
	// The routing defect is decided from the branch alone: the factory is
	// asked nothing.
	if requestSeen(requests, http.MethodGet, "/api/deployment") {
		t.Errorf("an unresolvable routing still asked the factory what it ships")
	}
}

// The named-config half of the submission preflight (tick tda): the branch may
// declare more than one complete routing — one epic on GLM, another on
// claude through the operator's subscription — and a config that cannot route
// is refused at the knee, before anything is pushed or booted, naming the
// config. The one refusal the acceptance names specially: a claude config
// with no subscription token configured, which would otherwise silently step
// every dispatch down to Workers AI.

// preflightConfiguredOverlay is the repository's own cloud shape with the
// named configs added: glm (the declared default, Workers AI) and claude
// (the subscription rung — implement on sonnet climbing to opus, review and
// close-out on opus, the ladder the operator named 2026-10-06).
func preflightConfiguredOverlay() string {
	return `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.glm.roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"

[configs.claude.roles.implement.tiers.economy]
model = "sonnet"

[configs.claude.roles.implement.tiers.strong]
model = "opus"

[configs.claude.roles.review]
kind = "claude"
model = "opus"

[configs.claude.roles.review.tiers.strong]
kind = "claude"
model = "opus"

[configs.claude.roles.closeout]
kind = "claude"
model = "opus"

[configs.claude.tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[configs.claude.tier_policy.concurrency]
economy = 2
strong = 1

[roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2
`
}

// labelEpic gives the fixture's epic1 a `config:` label at HEAD — the
// boundary the factory clones and the submission preflight reads.
func labelEpic(t *testing.T, repo, label string) {
	t.Helper()
	writeCloudTickFixture(t, repo, cloudTickFixture{
		ID: "epic1", Title: "Cloud test epic", Type: "epic",
		Owner: "operator", CreatedBy: "operator", Labels: []string{label},
	})
	execTestCmd(t, repo, "git", "add", ".tick")
	execTestCmd(t, repo, "git", "commit", "-m", "label the epic")
	execTestCmd(t, repo, "git", "push", "origin", "main")
}

// TestRunCloudRefusesAClaudeConfigWithNoSubscription is the acceptance's own
// refusal: an epic on a cloud config riding the claude-sub rung and a
// factory holding no subscription token — a run on that config would
// silently step every dispatch down to Workers AI. The submission is
// refused naming the config, the rung and the `wrangler secret put` that
// turns the rung on, and the only factory traffic is the reads.
func TestRunCloudRefusesAClaudeConfigWithNoSubscription(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightConfiguredOverlay())
	labelEpic(t, repo, "config: claude")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			// The image ships claude (the rung's harness) and holds NO
			// subscription token: the field is present and empty, which is
			// the rung being off — not absent, which is a factory that
			// predates the field.
			return deploymentAnswerWithSubscriptions("v1.0.0-7-gdeadbeef0123",
				[]string{"omp", "claude", "pi-durable"}, []string{})
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			t.Errorf("the preflight refused nothing: the factory was asked to start a run")
			return 201, map[string]any{"run": map[string]any{"run_id": "dd44", "state": "starting"}}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitGeneric {
		t.Fatalf("exit %d, want the failure class %d:\nstdout:\n%s\nstderr:\n%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	joined := stdout.String() + stderr.String()
	for _, want := range []string{
		"config claude rides the claude-sub subscription rung", // the config, named
		"implement-tick (tier economy)",                        // the jobs that ride it
		"no subscription token (CLAUDE_SUB_TOKEN_<LABEL>)",     // the rung being off
		"silently step every dispatch down to Workers AI",      // what would happen instead
		"wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>",         // the fix
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, joined)
		}
	}
	if len(rec.runIDs) != 0 {
		t.Errorf("the refused submission attached to a run: %v", rec.runIDs)
	}
}

// TestRunCloudSubmitsAGLMEpicBesideAnUnservedClaudeConfig: the same branch
// and the same factory with the rung off, and an epic that says nothing — so
// it runs on the declared default, glm, which rides no rung. The claude
// config the branch also declares is doctor's to hold to the subscription,
// not a reason to refuse an epic that will never ride it: "another epic
// runs on the default GLM config", unchanged.
func TestRunCloudSubmitsAGLMEpicBesideAnUnservedClaudeConfig(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightConfiguredOverlay())
	started := cloudRunIDOf("ff66")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return deploymentAnswerWithSubscriptions("v1.0.0-7-gdeadbeef0123",
				[]string{"omp", "claude", "pi-durable"}, []string{})
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d, want the GLM epic submitted:\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != started {
		t.Errorf("attached to %v, want [%s]", rec.runIDs, started)
	}
}

// TestRunCloudSubmitsAClaudeConfigWithASubscription: the same branch, the same
// factory, one difference — the factory holds a subscription token, so the
// rung is on and the config can be served. The preflight passes silently and
// the submission goes through: an epic on claude is a submission the factory
// can take, not one the preflight second-guesses.
func TestRunCloudSubmitsAClaudeConfigWithASubscription(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	commitPreflightConfig(t, repo, preflightCommonRunners, preflightConfiguredOverlay())
	labelEpic(t, repo, "config: claude")
	started := cloudRunIDOf("ee55")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return deploymentAnswerWithSubscriptions("v1.0.0-7-gdeadbeef0123",
				[]string{"omp", "claude", "pi-durable"}, []string{"max"})
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return 201, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d, want the success class the preflight passed into:\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != started {
		t.Errorf("attached to %v, want [%s]", rec.runIDs, started)
	}
}

// TestRunCloudRefusesAConfigThatCannotRoute: a named config with broken cells
// — the rung's harness on a PINNED model id, which bills per token in the
// cloud exactly as firmly as claude on any other harness — is refused naming
// the CONFIG, before a run can select it, with the same refusal a run's own
// start would give.
func TestRunCloudRefusesAConfigThatCannotRoute(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	overlay := `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.broken.roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.broken.roles.review]
kind = "claude"
model = "claude-sonnet-5-5"

[configs.broken.tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[roles.implement]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[roles.review]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
`
	commitPreflightConfig(t, repo, preflightCommonRunners, overlay)

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodGet && request.Path == "/api/deployment":
			return deploymentAnswer("v1.0.0-7-gdeadbeef0123", []string{"omp", "claude", "pi-durable"})
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			t.Errorf("the preflight refused nothing: the factory was asked to start a run")
			return 201, map[string]any{"run": map[string]any{"run_id": "ff66", "state": "starting"}}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitGeneric {
		t.Fatalf("exit %d, want the failure class %d:\nstdout:\n%s\nstderr:\n%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	joined := stdout.String() + stderr.String()
	for _, want := range []string{
		`the named run config "broken" cannot route`, // the config, named
		"pinned model id bills per token",            // the cell the error names
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, joined)
		}
	}
	if len(rec.runIDs) != 0 {
		t.Errorf("the refused submission attached to a run: %v", rec.runIDs)
	}
}
