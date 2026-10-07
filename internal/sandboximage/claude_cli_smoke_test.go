//go:build !windows

package sandboximage

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The REAL claude CLI, at the version image/Dockerfile pins, run the way a
// claude-sub worker runs it (tick 6fv): as root in a container, with the
// placeholder OAuth token, IS_SANDBOX, no nonessential traffic, and its
// traffic to api.anthropic.com answered by a server whose certificate a CA in
// NODE_EXTRA_CA_CERTS signs — which is exactly how production reaches the
// factory's ClaudeSubProxy (Containers' interception CA at
// /etc/cloudflare/certs/cloudflare-containers-ca.crt), with a fake standing in
// for the proxy. No ANTHROPIC_BASE_URL: production sets none, and the CLI
// talks to a different set of endpoints when one is set.
//
// Every other claude-sub test drives a stub, and the first production run
// died on what only the real binary does (its root check, epic ilz). This one
// pins what the real binary does on the wire:
//
//   - it gets past the root check under IS_SANDBOX (and is refused without
//     it — the negative control that says the check is real);
//   - every request it makes at startup and per turn is on the proxy's
//     allowlist (claudeSubRoutes, the copy of cloudflare/src/claude-sub.ts's
//     CLAUDE_SUB_ROUTES), so a pin bump that adds an endpoint fails HERE
//     instead of 403ing a live job;
//   - the aliases resolve to concrete models (logged, so a pin bump's
//     model move is visible in the test output);
//   - a quota 429 ends the process non-zero quickly, without a retry storm;
//   - `--session-id <uuid>` then `--resume <uuid>` (worker.sh's nudge argv)
//     carries the first turn's history into the second.
//
// It needs docker and the network (npm, the node base image), so it is
// opt-in: TICFAC_LIVE_CLAUDE_CLI=1, and run it whenever CLAUDE_CODE_VERSION
// moves. It is end-to-end as well (shorttest.EndToEnd), so the gate never
// pays for it.

// claudeSubRoutes is the claude-sub proxy's allowlist, a copy of
// CLAUDE_SUB_ROUTES in cloudflare/src/claude-sub.ts: method and path (no
// query). Change both together.
var claudeSubRoutes = map[string]bool{
	"POST /v1/messages":                  true,
	"POST /v1/messages/count_tokens":     true,
	"GET /api/claude_code/policy_limits": true,
	"GET /api/claude_code/settings":      true,
	// The CLI's connectivity check: 2.1.227 sends it on every start in a
	// Linux container, with no credential and no beta.
	"HEAD /api/hello": true,
}

// unauthenticatedRoutes are the allowlisted routes the CLI sends with no
// credential at all; every other route must carry the placeholder.
var unauthenticatedRoutes = map[string]bool{"HEAD /api/hello": true}

const smokeEnv = "TICFAC_LIVE_CLAUDE_CLI"

var claudeCodeVersionArg = regexp.MustCompile(`(?m)^ARG CLAUDE_CODE_VERSION=(\S+)$`)

type smokeRequest struct {
	Phase   string `json:"phase"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Beta    string `json:"beta"`
	Model   string `json:"model"`
	Auth    string `json:"auth"`
	Prompts string `json:"prompts"`
	At      int64  `json:"at"`
}

func TestTheRealClaudeCLIOnTheSubscriptionRoute(t *testing.T) {
	shorttest.EndToEnd(t)
	if os.Getenv(smokeEnv) != "1" {
		t.Skipf("set %s=1 to run the real claude CLI in docker (needs docker and the network)", smokeEnv)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker on PATH")
	}
	m := claudeCodeVersionArg.FindStringSubmatch(readDockerfile(t))
	if m == nil {
		t.Fatal("image/Dockerfile declares no ARG CLAUDE_CODE_VERSION")
	}
	version := m[1]

	tag := "ticfac-claude-cli-smoke:" + version
	build := exec.Command("docker", "build", "-t", tag, "-")
	build.Stdin = strings.NewReader("FROM node:22-slim\nRUN npm install -g @anthropic-ai/claude-code@" + version +
		" && claude --version\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the smoke image for claude %s: %v\n%s", version, err, out)
	}

	dir := t.TempDir()
	writeSmokeCerts(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "fake.js"), []byte(smokeFake), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(smokeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("docker", "run", "--rm",
		"--add-host", "api.anthropic.com:127.0.0.1",
		"-v", dir+":/smoke",
		"-e", "SID=3b2f9d4e-6a1c-4f0e-9b7d-2c5e8a1f0d93",
		tag, "bash", "/smoke/run.sh")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the smoke container failed: %v\n%s", err, out)
	}
	t.Logf("container output:\n%s", out)

	code := func(phase string) int {
		b, err := os.ReadFile(filepath.Join(dir, phase+".code"))
		if err != nil {
			t.Fatalf("no exit code for phase %s: %v", phase, err)
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	said := func(phase string) string {
		b, _ := os.ReadFile(filepath.Join(dir, phase+".out"))
		return string(b)
	}
	reqs := readSmokeRequests(t, filepath.Join(dir, "requests.log"))
	byPhase := map[string][]smokeRequest{}
	for _, r := range reqs {
		byPhase[r.Phase] = append(byPhase[r.Phase], r)
		t.Logf("%-7s %s %s model=%q beta=%q", r.Phase, r.Method, r.Path, r.Model, r.Beta)
	}

	// The negative control: without IS_SANDBOX the CLI refuses root, so
	// the assertion below is about the variable, not about luck.
	if code("noroot") == 0 || !strings.Contains(strings.ToLower(said("noroot")), "root") {
		t.Errorf("without IS_SANDBOX the CLI as root did not refuse (exit %d) — the root check this test "+
			"guards has changed:\n%s", code("noroot"), said("noroot"))
	}
	// Past the root check, through the interception's CA, to an answer.
	for _, phase := range []string{"first", "resume", "opus"} {
		if code(phase) != 0 || !strings.Contains(said(phase), "READY") {
			t.Errorf("phase %s: the CLI as root with IS_SANDBOX=1 exited %d without the fake's answer:\n%s",
				phase, code(phase), said(phase))
		}
	}

	// Every request is on the proxy's allowlist, carries the placeholder,
	// and the OAuth beta; none asks for the 1M-context beta the proxy refuses.
	seen := map[string]bool{}
	for _, r := range reqs {
		route := r.Method + " " + strings.SplitN(r.Path, "?", 2)[0]
		seen[route] = true
		if !claudeSubRoutes[route] {
			t.Errorf("the CLI called %s (phase %s), which the claude-sub proxy's allowlist refuses with 403", route, r.Phase)
		}
		if unauthenticatedRoutes[route] {
			continue
		}
		if r.Auth != "Bearer "+claudeSubPlaceholder {
			t.Errorf("%s carried authorization %q, not the placeholder", route, r.Auth)
		}
		if !strings.Contains(r.Beta, "oauth-2025-04-20") {
			t.Errorf("%s carried no OAuth beta (%q)", route, r.Beta)
		}
		if strings.Contains(r.Beta, "context-1m") {
			t.Errorf("%s asked for the 1M-context beta, which the proxy refuses (%q)", route, r.Beta)
		}
	}
	routes := make([]string, 0, len(seen))
	for r := range seen {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	t.Logf("routes the claude %s CLI called: %s", version, strings.Join(routes, ", "))

	// The aliases resolve inside the pinned CLI to concrete models.
	for phase, alias := range map[string]string{"first": "sonnet", "opus": "opus"} {
		model := ""
		for _, r := range byPhase[phase] {
			if r.Path == "/v1/messages" || strings.HasPrefix(r.Path, "/v1/messages?") {
				model = r.Model
			}
		}
		if !strings.HasPrefix(model, "claude-"+alias) {
			t.Errorf("--model %s reached the wire as %q, want a claude-%s-* model", alias, model, alias)
		} else {
			t.Logf("claude %s resolves %s to %s", version, alias, model)
		}
	}

	// --resume carries the first turn into the second.
	resumed := false
	for _, r := range byPhase["resume"] {
		if strings.Contains(r.Prompts, "SMOKE-FIRST-PROMPT") && strings.Contains(r.Prompts, "SMOKE-SECOND-PROMPT") {
			resumed = true
		}
	}
	if !resumed {
		t.Errorf("--resume did not send the first turn's history with the second prompt")
	}

	// A quota 429 ends the job quickly and non-zero, with no retry storm.
	if code("quota") == 0 {
		t.Errorf("a quota 429 left the CLI exiting 0:\n%s", said("quota"))
	}
	messages := 0
	for _, r := range byPhase["quota"] {
		if strings.HasPrefix(r.Path, "/v1/messages") && !strings.HasPrefix(r.Path, "/v1/messages/count_tokens") {
			messages++
		}
	}
	if messages == 0 || messages > 3 {
		t.Errorf("a quota 429 drew %d /v1/messages requests, want 1-3 (no retry storm)", messages)
	}
	if secs, _ := os.ReadFile(filepath.Join(dir, "quota.secs")); len(secs) > 0 {
		if n, _ := strconv.Atoi(strings.TrimSpace(string(secs))); n > 30 {
			t.Errorf("a quota 429 took %ds to end the CLI, want under 30s", n)
		}
	}
}

func readSmokeRequests(t *testing.T, path string) []smokeRequest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the fake recorded no requests: %v", err)
	}
	defer f.Close()
	var out []smokeRequest
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r smokeRequest
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("a request record is not JSON: %v: %s", err, sc.Text())
		}
		out = append(out, r)
	}
	return out
}

// writeSmokeCerts writes a throwaway CA and an api.anthropic.com leaf it
// signs: the fake's certificate, and the CA the CLI is told to trust.
func writeSmokeCerts(t *testing.T, dir string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ticfac claude-sub smoke CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		DNSNames:     []string{"api.anthropic.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"ca.crt":   {Type: "CERTIFICATE", Bytes: caDER},
		"leaf.crt": {Type: "CERTIFICATE", Bytes: leafDER},
		"leaf.key": {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// smokeScript runs inside the container, as root: the fake on :443, then the
// CLI in each phase. Every phase's exit code and output land in /smoke.
const smokeScript = `set -u
cd /smoke
node /smoke/fake.js &
for i in $(seq 100); do [ -f /smoke/ready ] && break; sleep 0.1; done
export CLAUDE_CODE_OAUTH_TOKEN=` + claudeSubPlaceholder + `
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
export CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1
export NODE_EXTRA_CA_CERTS=/smoke/ca.crt
unset ANTHROPIC_BASE_URL ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
mkdir -p /work && cd /work
phase() { printf '%s' "$1" > /smoke/phase; }
record() { echo "$?" > "/smoke/$1.code"; }

phase noroot
timeout 60 claude -p --dangerously-skip-permissions --model sonnet "Reply READY" > /smoke/noroot.out 2>&1; record noroot

export IS_SANDBOX=1
phase first
timeout 120 claude -p --session-id "$SID" --dangerously-skip-permissions --model sonnet "SMOKE-FIRST-PROMPT reply READY" > /smoke/first.out 2>&1; record first
phase resume
timeout 120 claude -p --resume "$SID" --dangerously-skip-permissions --model sonnet "SMOKE-SECOND-PROMPT reply READY" > /smoke/resume.out 2>&1; record resume
phase opus
timeout 120 claude -p --dangerously-skip-permissions --model opus "Reply READY" > /smoke/opus.out 2>&1; record opus

phase quota
printf 429 > /smoke/mode
start=$(date +%s)
timeout 120 claude -p --dangerously-skip-permissions --model sonnet "Reply READY" > /smoke/quota.out 2>&1; record quota
echo $(( $(date +%s) - start )) > /smoke/quota.secs
exit 0
`

// smokeFake is api.anthropic.com as the CLI sees it through the interception:
// it records every request (phase, method, path, betas, model, the user
// prompts' text) and answers /v1/messages with a one-word turn — or, in 429
// mode, with the quota rejection a spent subscription answers.
const smokeFake = `const https = require("https");
const fs = require("fs");
const read = (f, d) => { try { return fs.readFileSync(f, "utf8"); } catch { return d; } };
const text = (c) => typeof c === "string" ? c : Array.isArray(c) ? c.map((p) => p && p.text ? p.text : "").join(" ") : "";
const server = https.createServer({ key: fs.readFileSync("/smoke/leaf.key"), cert: fs.readFileSync("/smoke/leaf.crt") }, (req, res) => {
  let body = "";
  req.on("data", (c) => { body += c; });
  req.on("end", () => {
    let parsed = {};
    try { parsed = JSON.parse(body || "{}"); } catch {}
    const prompts = (parsed.messages || []).filter((m) => m.role === "user").map((m) => text(m.content)).join(" | ");
    fs.appendFileSync("/smoke/requests.log", JSON.stringify({
      phase: read("/smoke/phase", ""), method: req.method, path: req.url,
      beta: req.headers["anthropic-beta"] || "", model: parsed.model || "",
      auth: req.headers["authorization"] || "", prompts, at: Date.now(),
    }) + "\n");
    const path = req.url.split("?")[0];
    if (path === "/v1/messages" && read("/smoke/mode", "") === "429") {
      res.writeHead(429, { "content-type": "application/json",
        "anthropic-ratelimit-unified-status": "rejected",
        "anthropic-ratelimit-unified-reset": String(Math.floor(Date.now() / 1000) + 3600),
        "anthropic-ratelimit-unified-representative-claim": "five_hour",
        "retry-after": "3600" });
      return res.end(JSON.stringify({ type: "error", error: { type: "rate_limit_error", message: "This request would exceed your account's rate limit." } }));
    }
    if (path === "/v1/messages") {
      const model = parsed.model || "x";
      if (parsed.stream) {
        const ev = [
          ["message_start", { type: "message_start", message: { id: "msg_1", type: "message", role: "assistant", model, content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 } } }],
          ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
          ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "READY" } }],
          ["content_block_stop", { type: "content_block_stop", index: 0 }],
          ["message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } }],
          ["message_stop", { type: "message_stop" }],
        ];
        res.writeHead(200, { "content-type": "text/event-stream" });
        return res.end(ev.map(([e, d]) => "event: " + e + "\ndata: " + JSON.stringify(d) + "\n\n").join(""));
      }
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({ id: "msg_1", type: "message", role: "assistant", model, content: [{ type: "text", text: "READY" }], stop_reason: "end_turn", stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 } }));
    }
    if (path === "/v1/messages/count_tokens") {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end('{"input_tokens":1}');
    }
    res.writeHead(404, { "content-type": "application/json" });
    res.end("{}");
  });
});
server.listen(443, "127.0.0.1", () => fs.writeFileSync("/smoke/ready", "1"));
`
