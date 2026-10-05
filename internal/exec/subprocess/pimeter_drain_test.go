package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The drain suite (tick 648, epic hn6): the AI Gateway writes a call's log
// row only once the client has read the streamed response to its end — a
// stream abandoned after `finish_reason` is a call the logs never show, and
// a local run's Workers AI line would stay "not metered" however well the
// override tags it. dm2 suspected pi of exactly that ("pi stops consuming
// after finish_reason"); tick 648 found otherwise, live and here: pi-ai's
// openai-completions loop iterates the SDK stream to EOF, and the SDK reads
// past [DONE] to the end of the body. These tests pin that, so a pi upgrade
// that starts abandoning the tail fails here instead of emptying the metered
// half of the cost line in silence.

// drainGap is how long the fake gateway holds the tail of the stream back
// after `finish_reason`: long enough that a client which stops at the finish
// has closed its connection (or exited) before the tail is written.
const drainGap = 1500 * time.Millisecond

// drainUsage is the usage the tail chunk reports, numbers no other chunk
// carries: a client that reports them read the tail.
const (
	drainPromptTokens     = 4321
	drainCompletionTokens = 123
)

// fakeGateway serves the workers-ai route's OpenAI-compatible stream in the
// shape the real gateway sends it — content, then `finish_reason`, then a
// usage-only chunk and [DONE] — with the tail held back by gap, and records
// whether the client was still there for it.
type fakeGateway struct {
	gap time.Duration

	mu        sync.Mutex
	requests  int
	path      string
	header    http.Header
	abandoned bool // the client went away before the tail was written
	served    bool // the whole stream, tail and [DONE], was written to a live client
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	g.mu.Lock()
	g.requests++
	g.path = r.URL.Path
	g.header = r.Header.Clone()
	g.mu.Unlock()

	flusher, _ := w.(http.Flusher)
	send := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(choices string, extra string) string {
		return `{"id":"chatcmpl-648","object":"chat.completion.chunk","created":1,"model":"@cf/zai-org/glm-5.3","choices":` + choices + extra + `}`
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	send(chunk(`[{"index":0,"delta":{"role":"assistant","content":"pong"},"finish_reason":null}]`, ""))
	send(chunk(`[{"index":0,"delta":{},"finish_reason":"stop"}]`, ""))

	select {
	case <-r.Context().Done():
		g.mu.Lock()
		g.abandoned = true
		g.mu.Unlock()
		return
	case <-time.After(g.gap):
	}
	send(chunk(`[]`, fmt.Sprintf(`,"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}`,
		drainPromptTokens, drainCompletionTokens, drainPromptTokens+drainCompletionTokens)))
	send("[DONE]")

	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Context().Err() != nil {
		g.abandoned = true
		return
	}
	g.served = true
}

func (g *fakeGateway) state() (requests int, path string, header http.Header, abandoned, served bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests, g.path, g.header, g.abandoned, g.served
}

// The detector first: a test that cannot fail proves nothing, so the fake
// must see a client that stops reading at `finish_reason` — the behaviour
// dm2 suspected — as abandoning the stream.
func TestFakeGatewaySeesAClientThatStopsAtTheFinish(t *testing.T) {
	t.Parallel()
	gateway := &fakeGateway{gap: 2 * time.Second}
	server := httptest.NewServer(gateway)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/acct/gw/workers-ai/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post to the fake gateway: %v", err)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"finish_reason":"stop"`) {
			break
		}
	}
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, _, abandoned, served := gateway.state()
		if abandoned {
			return
		}
		if served {
			t.Fatal("the fake gateway counted a client that stopped at finish_reason as having read the whole stream: the drain test below could never fail")
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake gateway never noticed the client leave after finish_reason")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pi itself, launched with the override WriteExtension generates, against a
// fake gateway: the request carries the join (route, run id, credential
// header), and pi reads the stream past `finish_reason` to the usage tail and
// [DONE] — the read the real gateway needs before it writes the call's row.
func TestPiDrainsTheGatewayStreamThroughTheMeteringOverride(t *testing.T) {
	shorttest.EndToEnd(t)
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not on PATH: the drain is pi's behaviour, and only a host that runs pi workers can show it")
	}

	gateway := &fakeGateway{gap: drainGap}
	server := httptest.NewServer(gateway)
	defer server.Close()

	// A sealed pi: its own config directory, a HOME whose ~/.ticfacrc holds
	// a fake token (the override's credential command reads it there), and
	// a fake Workers AI credential in the environment, so nothing of the
	// operator's — auth, models, extensions, gateway — is read or reached.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".ticfacrc"), []byte("factory_cloudflare_api_token=fake-account-token\n"), 0o600); err != nil {
		t.Fatalf("write the sealed ~/.ticfacrc: %v", err)
	}
	metering := &GatewayMetering{RunID: "run-648-drain", GatewayURL: server.URL + "/v1/acct/gw"}
	extension, err := metering.WriteExtension(t.TempDir())
	if err != nil {
		t.Fatalf("write the metering extension: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := append(metering.ExtensionArgs(extension),
		"--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-context-files", "--no-prompt-templates",
		"--mode", "json", "--model", "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "-p", "Reply with the single word pong.")
	cmd := exec.CommandContext(ctx, pi, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PI_CODING_AGENT_DIR="+filepath.Join(home, ".pi-agent"),
		"PI_OFFLINE=1",
		"CLOUDFLARE_API_KEY=fake-workers-ai-key",
		"CLOUDFLARE_ACCOUNT_ID=fake-account",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pi exited with %v\nstderr:\n%s\nstdout:\n%s", err, stderr.String(), stdout.String())
	}

	requests, path, header, abandoned, served := gateway.state()
	if requests != 1 {
		t.Fatalf("the fake gateway saw %d requests, want 1 — pi retried or never routed through the override\nstdout:\n%s", requests, stdout.String())
	}
	if path != "/v1/acct/gw/workers-ai/v1/chat/completions" {
		t.Errorf("pi called %s, want the gateway's workers-ai/v1 route", path)
	}
	if got := header.Get(metadataHeader); got != `{"run_id":"run-648-drain"}` {
		t.Errorf("pi sent %s %q, want the run id the reader joins on", metadataHeader, got)
	}
	if got := header.Get(gatewayAuthHeader); got != "Bearer fake-account-token" {
		t.Errorf("pi sent %s %q, want the ~/.ticfacrc token resolved at request time", gatewayAuthHeader, got)
	}
	// The displacement (tick m4t): the Authorization the request carries
	// must be the override's account token, NOT the credential pi itself
	// resolved (here the fake key in CLOUDFLARE_API_KEY the sealed pi
	// reads) — the gateway forwards that header to Workers AI, and a key
	// that is not valid upstream fails the call beside a valid
	// cf-aig-authorization (live, tick 648 probe e: upstream code 10000).
	// A metered dispatch that left pi's stored key in place would 401 on
	// exactly the host this join exists for.
	if got := header.Get("Authorization"); got != "Bearer fake-account-token" {
		t.Errorf("pi sent Authorization %q, want the override's account token displacing the key pi resolved on its own (CLOUDFLARE_API_KEY=fake-workers-ai-key)", got)
	}
	if abandoned || !served {
		t.Fatalf("pi left the stream before its tail (abandoned=%v, served=%v): the gateway writes no log row for a call whose stream is not drained, so this run's Workers AI spend would never meter", abandoned, served)
	}

	// The tail was not only written to a live connection, it was READ: the
	// usage pi reports is the usage only the tail chunk carried.
	if !piReportedUsage(t, stdout.Bytes(), drainPromptTokens, drainCompletionTokens) {
		t.Fatalf("pi never reported the usage the tail chunk carried (input %d, output %d): it did not read past finish_reason\nstdout:\n%s",
			drainPromptTokens, drainCompletionTokens, stdout.String())
	}
}

// piReportedUsage reports whether pi's json-mode event stream ends an
// assistant message with exactly this usage.
func piReportedUsage(t *testing.T, events []byte, input, output int) bool {
	t.Helper()
	for _, line := range bytes.Split(events, []byte("\n")) {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role  string `json:"role"`
				Usage struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Type == "message_end" && event.Message.Role == "assistant" &&
			event.Message.Usage.Input == input && event.Message.Usage.Output == output {
			return true
		}
	}
	return false
}
