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
// its fix.
func TestAGatewayRefusalIsNotRetried(t *testing.T) {
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_CURL_STATUS"] = "503"
	f.env[EnvModelProbeBackoff] = "30"
	out, code := f.run()
	if code != ExitModel {
		t.Fatalf("a worker whose gateway refused exited %d, want %d (ExitModel):\n%s", code, ExitModel, out)
	}
	if tries := strings.Count(readFile(t, f.env["TICKS_TEST_CURL_RECORD"]), "\n"); tries != 1 {
		t.Errorf("a refusal was asked %d times, want once", tries)
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
