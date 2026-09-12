package factory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// The assertion itself
//
// A gateway's workers_ai_billing_mode decides which pot a run's Workers AI
// spend comes out of: `postpaid` reaches the Cloudflare invoice an account
// credit can absorb, `unified` drains a separately purchased prepaid wallet
// bought at a 5% premium. It is one dashboard toggle, it is not in
// wrangler.toml, and nothing in a run's telemetry records which one was in
// force — so the only place it can be caught is before a run starts.
// ---------------------------------------------------------------------------

// The fakes below are the billing-facing half of the harness that stays in
// ticks (internal/factory/setup_test.go) until the setup/status ticks move it
// (ticks b3a / 0e1); copied here so the moved assertion is exercised by the
// same servers it was in ticks. When setup_test.go moves, it must take or
// unify these copies rather than ship a second set.

const (
	// testGatewayID/testGatewayNS shape a gateway URL the way the real
	// operator's ~/.ticfacrc does: <base>/v1/<account>/<gateway>.
	testGatewayID = "ticks"
	// testCloudflareToken stands in for the credential the telemetry rung
	// installs; it never leaves the test process.
	testCloudflareToken = "cf_api_token_EXAMPLE"
)

var testGatewayNS = "/v1/00000000000000000000000000000000/" + testGatewayID

// fakeGateway is the model API behind the operator's AI Gateway URL. The
// billing check never talks to it — the gateway URL names the account and
// gateway, and the MODE is read off the Cloudflare API — but the URL must be
// shaped like a real one.
type fakeGateway struct {
	server *httptest.Server
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{}
	gw.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/compat/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{
				map[string]any{"id": "model-alpha"},
				map[string]any{"id": "model-beta"},
			},
		})
	}))
	t.Cleanup(gw.server.Close)
	return gw
}

func (gw *fakeGateway) base() string { return gw.server.URL + testGatewayNS }

// fakeCloudflareAPI answers the AI Gateway read the billing assertion makes
// with a Cloudflare API token: the gateway OBJECT, whose
// workers_ai_billing_mode is the whole question.
type fakeCloudflareAPI struct {
	server *httptest.Server
	token  string
	// gatewayCalls counts reads of the gateway OBJECT, so a test can tell a
	// billing assertion that ran from one that was skipped.
	gatewayCalls atomic.Int32

	mu sync.Mutex
	// billingMode is what the gateway object reports. Empty means the object
	// omits the field entirely, which is its own failure class.
	billingMode string
}

func newFakeCloudflareAPI(t *testing.T, token string) *fakeCloudflareAPI {
	t.Helper()
	api := &fakeCloudflareAPI{token: token, billingMode: BillingModePostpaid}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+api.token {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []any{map[string]any{"message": "Authentication error"}},
			})
			return
		}
		if id, ok := gatewayObjectPath(r.URL.Path); ok {
			api.gatewayCalls.Add(1)
			if id != testGatewayID {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false,
					"errors":  []any{map[string]any{"message": "gateway not found"}},
				})
				return
			}
			result := map[string]any{"id": id}
			if mode := api.mode(); mode != "" {
				result["workers_ai_billing_mode"] = mode
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(api.server.Close)
	return api
}

// gatewayObjectPath reports the gateway id a GET of the gateway OBJECT names —
// /accounts/<account>/ai-gateway/gateways/<id> with nothing after it.
func gatewayObjectPath(path string) (string, bool) {
	const marker = "/ai-gateway/gateways/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return "", false
	}
	id := strings.Trim(strings.TrimPrefix(path[idx:], marker), "/")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func (api *fakeCloudflareAPI) mode() string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.billingMode
}

func (api *fakeCloudflareAPI) setMode(mode string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.billingMode = mode
}

func (api *fakeCloudflareAPI) base() string { return api.server.URL }

type billingHarness struct {
	gateway    *fakeGateway
	cloudflare *fakeCloudflareAPI
}

func newBillingHarness(t *testing.T) *billingHarness {
	t.Helper()
	return &billingHarness{
		gateway:    newFakeGateway(t),
		cloudflare: newFakeCloudflareAPI(t, testCloudflareToken),
	}
}

func (h *billingHarness) billingOptions() BillingOptions {
	return BillingOptions{
		CloudflareAPIBase:  h.cloudflare.base(),
		GatewayURL:         h.gateway.base(),
		CloudflareAPIToken: testCloudflareToken,
	}
}

func TestBillingCheckAcceptsThePostpaidDefault(t *testing.T) {
	h := newBillingHarness(t)

	mode, err := CheckWorkersAIBilling(context.Background(), h.billingOptions())
	if err != nil {
		t.Fatalf("CheckWorkersAIBilling: %v", err)
	}
	if mode != BillingModePostpaid {
		t.Errorf("mode = %q, want %q", mode, BillingModePostpaid)
	}
	if h.cloudflare.gatewayCalls.Load() == 0 {
		t.Error("the mode was reported without reading the gateway object")
	}
}

// The headline failure: the dashboard was flipped and every run since has been
// spending cash instead of credit.
func TestBillingCheckRefusesUnifiedAndSaysWhatItCosts(t *testing.T) {
	h := newBillingHarness(t)
	h.cloudflare.setMode(BillingModeUnified)

	mode, err := CheckWorkersAIBilling(context.Background(), h.billingOptions())
	if err == nil {
		t.Fatal("a gateway on unified billing passed pre-flight")
	}
	if mode != BillingModeUnified {
		t.Errorf("mode = %q, want the observed mode reported alongside the refusal", mode)
	}
	// Loud means it names both modes, the money, and the two ways out.
	for _, want := range []string{
		BillingModeUnified,
		BillingModePostpaid,
		"prepaid",
		"5%",
		"--workers-ai-billing-mode",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal never mentions %q: %v", want, err)
		}
	}
}

// An operator who deliberately bought a prepaid wallet is not misconfigured —
// the assertion is against what they configured, not against a constant.
func TestBillingCheckHonoursAnOperatorWhoChoseUnified(t *testing.T) {
	h := newBillingHarness(t)
	h.cloudflare.setMode(BillingModeUnified)

	opts := h.billingOptions()
	opts.Expected = BillingModeUnified
	mode, err := CheckWorkersAIBilling(context.Background(), opts)
	if err != nil {
		t.Fatalf("CheckWorkersAIBilling: %v", err)
	}
	if mode != BillingModeUnified {
		t.Errorf("mode = %q, want %q", mode, BillingModeUnified)
	}

	// And the mirror: postpaid is a mismatch when unified is what was chosen.
	h.cloudflare.setMode(BillingModePostpaid)
	if _, err := CheckWorkersAIBilling(context.Background(), opts); err == nil {
		t.Fatal("drift away from the configured mode passed pre-flight")
	}
}

// A gateway object that names no mode is not a gateway that is on postpaid: an
// assertion that cannot read the mode has not passed, and says so in its own
// words rather than borrowing the drift message.
func TestBillingCheckRefusesAGatewayThatNamesNoMode(t *testing.T) {
	h := newBillingHarness(t)
	h.cloudflare.setMode("")

	_, err := CheckWorkersAIBilling(context.Background(), h.billingOptions())
	if err == nil {
		t.Fatal("a gateway object with no workers_ai_billing_mode passed pre-flight")
	}
	if !strings.Contains(err.Error(), "workers_ai_billing_mode") {
		t.Errorf("error does not name the field it could not read: %v", err)
	}
	if strings.Contains(err.Error(), "5%") {
		t.Errorf("an unreadable mode was reported as a billing-mode drift: %v", err)
	}
}

// A typo in ~/.ticfacrc must not silently disable the assertion.
func TestBillingCheckRefusesAnUnknownConfiguredMode(t *testing.T) {
	h := newBillingHarness(t)
	opts := h.billingOptions()
	opts.Expected = "prepaid"

	if _, err := CheckWorkersAIBilling(context.Background(), opts); err == nil {
		t.Fatal("an unknown configured mode was accepted")
	} else if !strings.Contains(err.Error(), "prepaid") ||
		!strings.Contains(err.Error(), BillingModePostpaid) {
		t.Errorf("error does not name the bad value and the valid ones: %v", err)
	}
	if h.cloudflare.gatewayCalls.Load() != 0 {
		t.Error("an unreadable expectation still cost a live API call")
	}
}

// Four things can go wrong here and they need four answers, not one: the
// gateway is not a Cloudflare gateway, there is no token, the token is
// rejected, the gateway does not exist.
func TestBillingCheckKeepsItsFailureClassesApart(t *testing.T) {
	h := newBillingHarness(t)

	notAGateway := h.billingOptions()
	notAGateway.GatewayURL = "https://api.anthropic.com"
	_, err := CheckWorkersAIBilling(context.Background(), notAGateway)
	if err == nil || !strings.Contains(err.Error(), "no Cloudflare account") {
		t.Errorf("a non-Cloudflare gateway URL: %v", err)
	}

	noToken := h.billingOptions()
	noToken.CloudflareAPIToken = ""
	_, err = CheckWorkersAIBilling(context.Background(), noToken)
	if err == nil || !strings.Contains(err.Error(), "--cloudflare-api-token") {
		t.Errorf("a missing token does not point at the flag that supplies one: %v", err)
	}

	badToken := h.billingOptions()
	badToken.CloudflareAPIToken = "cf_wrong_token"
	_, err = CheckWorkersAIBilling(context.Background(), badToken)
	if err == nil || !strings.Contains(err.Error(), "AI Gateway read") {
		t.Errorf("a rejected token does not name the access it needs: %v", err)
	}

	missing := h.billingOptions()
	missing.GatewayURL = strings.TrimSuffix(h.gateway.base(), testGatewayID) + "no-such-gateway"
	_, err = CheckWorkersAIBilling(context.Background(), missing)
	if err == nil || !strings.Contains(err.Error(), "no-such-gateway") {
		t.Errorf("a gateway that does not exist: %v", err)
	}
}
