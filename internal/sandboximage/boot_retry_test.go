package sandboximage

import (
	"os"
	"strings"
	"testing"
)

// Epic hn6, run_37b36bfe (2026-10-01 ~04:49Z): 0rx's worker booted while the
// factory's Worker was being redeployed. Its one-token gateway probe got no
// answer in one 30s try, the boot exited 7 — the class that means "the route
// is broken" — and the run read that as a failed attempt at 0rx, escalating it
// two rungs to the ceiling. Nothing about the tick had been tried. The boot
// must ask the gateway again over a bounded window, and a gateway that never
// answers through it is its own infrastructure code, not the model verdict.

// A gateway that does not answer at first and then does is a blip, not a stop:
// the boot asks again and goes on to the harness.
func TestWorkerAsksAGatewayThatDidNotAnswerAgain(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS_SEQ"] = "000 502 200"
	f.env[EnvModelProbeBackoff] = "0"
	out, code := f.run()
	if code != 0 {
		t.Fatalf("a worker whose gateway answered on the third try exited %d, want 0:\n%s", code, out)
	}
	mustContain(t, out, "asking again", "the retry is in the worker's own log")
	mustContain(t, out, "model probe green", "the probe that answered")
	if f.harnessRecord() == "" {
		t.Error("the harness never started after the gateway answered")
	}
}

// A gateway that never answers through the window is EXIT_GATEWAY_UNAVAILABLE,
// never ExitModel: the orchestrator dispatches the job again at the same tier
// on this code, where ExitModel is a verdict a retry as-is reaches again.
func TestAGatewayThatNeverAnswersIsInfrastructureNotAModelVerdict(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS"] = "000"
	f.env[EnvModelProbeTries] = "2"
	f.env[EnvModelProbeBackoff] = "0"
	out, code := f.run()
	if code != ExitGatewayUnavailable {
		t.Fatalf("a worker whose gateway never answered exited %d, want %d (ExitGatewayUnavailable):\n%s",
			code, ExitGatewayUnavailable, out)
	}
	mustContain(t, out, "gateway unavailable", "the class the orchestrator reads")
	if tries := strings.Count(readFile(t, f.env["TICKS_TEST_CURL_RECORD"]), "\n"); tries < 2 {
		t.Errorf("the gateway was asked %d time(s): one try is what lost run_37b36bfe a rung\n%s", tries, out)
	}
	if f.harnessRecord() != "" {
		t.Error("the harness started on a gateway that never answered")
	}
}

// A gateway that ANSWERED no is the verdict it always was, at once: retrying a
// configuration refusal for minutes would only delay the message that names
// its fix. That covers the route's 4xx answers (a refused credential, an
// unknown model) and the factory gateway's own 503 configuration refusal,
// which names its fix in an `…_not_configured` error.
func TestAGatewayRefusalIsNotRetried(t *testing.T) {
	for _, tc := range []struct{ name, status, body string }{
		{"a refused credential", "401", `{"error":"unauthorized"}`},
		{"a forbidden route", "403", `{"error":"provider_not_opted_in"}`},
		{"an unknown model", "404", `{"errors":[{"message":"No such model @cf/zai-org/glm-nope","code":5007}]}`},
		{"the gateway's own configuration refusal", "503",
			`{"error":"provider_not_configured","detail":"this factory has no key for workers-ai; run 'ticfac factory setup'"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.env["TICKS_TEST_CURL_STATUS"] = tc.status
			f.env["TICKS_TEST_CURL_BODY"] = tc.body
			f.env[EnvModelProbeBackoff] = "30"
			out, code := f.run()
			if code != ExitModel {
				t.Fatalf("a worker whose gateway refused (HTTP %s) exited %d, want %d (ExitModel):\n%s",
					tc.status, code, ExitModel, out)
			}
			if tries := strings.Count(readFile(t, f.env["TICKS_TEST_CURL_RECORD"]), "\n"); tries != 1 {
				t.Errorf("a refusal was asked %d times, want once", tries)
			}
		})
	}
}

// incidentAiError is the body Workers AI answered the model probe with through
// the factory's gateway for twenty minutes of epic ymf's cloud run
// (run_91f2952a, 2026-10-09): a provider-side outage, not the route.
const incidentAiError = `{"name":"AiError","internalCode":4007,"httpCode":500,"message":"AiError: AiError: An internal server error occured. (8c1f0a3e-5b7d-4e2a-9f61-2d0c7b4e9a15)"}`

// ymf run_91f2952a: the orchestrator's container was evicted, and its two
// replacements' probes got Workers AI's 500 AiError 4007. Both exited 7 — "the
// route is broken, configure it with ticfac factory setup" — and the Workflow
// ended a run ten hours in after its third boot. Twenty minutes later the same
// route was green. A provider's server error is asked again over the probe's
// window, and one that never clears is infrastructure (14), never 7.
func TestAProviderServerErrorIsInfrastructureNotAModelVerdict(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS"] = "500"
	f.env["TICKS_TEST_CURL_BODY"] = incidentAiError
	f.env[EnvModelProbeBackoff] = "0"
	out, code := f.run()
	if code != ExitGatewayUnavailable {
		t.Fatalf("a worker whose provider answered 500 AiError on every try exited %d, want %d (ExitGatewayUnavailable):\n%s",
			code, ExitGatewayUnavailable, out)
	}
	if tries := strings.Count(readFile(t, f.env["TICKS_TEST_CURL_RECORD"]), "\n"); tries != 4 {
		t.Errorf("the gateway was asked %d time(s), want the default 4\n%s", tries, out)
	}
	mustContain(t, out, "asking again", "the retries are in the worker's own log")
	mustContain(t, out, "HTTP 500", "the stop names the status")
	mustContain(t, out, "internalCode", "the stop quotes the provider's body")
	if strings.Contains(out, "ticfac factory setup") {
		t.Errorf("a provider outage was reported as a configuration to fix:\n%s", out)
	}
	if f.harnessRecord() != "" {
		t.Error("the harness started on a provider that never answered")
	}
}

// A provider outage that clears inside the window is a green boot: the
// incident's 500, then a 503 from the upstream and Workers AI's capacity
// error, then an answer.
func TestAProviderServerErrorThatClearsIsAGreenBoot(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS_SEQ"] = "500 503 429 200"
	f.env["TICKS_TEST_CURL_BODY"] = incidentAiError
	f.env[EnvModelProbeBackoff] = "0"
	out, code := f.run()
	if code != 0 {
		t.Fatalf("a worker whose provider answered on the fourth try exited %d, want 0:\n%s", code, out)
	}
	mustContain(t, out, "model probe green", "the probe that answered")
	if f.harnessRecord() == "" {
		t.Error("the harness never started after the provider answered")
	}
}

// Workers AI's capacity error is transient whatever status it rides on.
func TestWorkersAICapacityErrorIsTransient(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS"] = "400"
	f.env["TICKS_TEST_CURL_BODY"] = `{"errors":[{"message":"AiError: Capacity temporarily exceeded, please try again.","code":3040}],"internalCode":3040}`
	f.env[EnvModelProbeTries] = "2"
	f.env[EnvModelProbeBackoff] = "0"
	out, code := f.run()
	if code != ExitGatewayUnavailable {
		t.Fatalf("a worker whose provider was out of capacity exited %d, want %d:\n%s", code, ExitGatewayUnavailable, out)
	}
}

// Origin that does not answer the fetch is asked again over its window, and a
// remote that never answers is EXIT_ORIGIN_UNAVAILABLE — infrastructure — not
// the clone verdict a refused credential is.
func TestAnOriginThatNeverAnswersIsInfrastructureNotACloneVerdict(t *testing.T) {
	f := newDoorFixture(t, true)
	// Nothing listens on port 1: every fetch is a refused connection, which is
	// origin not answering rather than origin saying no.
	f.url = "http://127.0.0.1:1/never/answers.git"
	f.extraEnv = []string{"TICKS_FETCH_WINDOW=2", "TICKS_BOOT_RETRY_BACKOFF=1"}
	out, code := f.clone(t)
	if code != ExitOriginUnavailable {
		t.Fatalf("a clone from an origin that never answered exited %d, want %d (ExitOriginUnavailable)\n%s",
			code, ExitOriginUnavailable, out)
	}
	if strings.Count(out, "fetching again") < 1 {
		t.Errorf("origin was asked once and given up on:\n%s", out)
	}
	mustContain(t, out, "origin unavailable", "the class the orchestrator reads")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
