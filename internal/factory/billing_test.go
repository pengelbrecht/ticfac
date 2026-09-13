package factory

import (
	"context"
	"strings"
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

// The billing-facing fakes (fakeGateway, fakeCloudflareAPI, the test gateway
// URL shape and the test Cloudflare token) live in setup_test.go, which moved
// with the deploy path in ticks tick b3a — they were ek7's copies of the
// harness the billing assertion ran against in ticks, and this file now
// shares them rather than shipping a second set (see the note that used to
// stand here).

type billingHarness struct {
	gateway    *fakeGateway
	cloudflare *fakeCloudflareAPI
}

func newBillingHarness(t *testing.T) *billingHarness {
	t.Helper()
	return &billingHarness{
		gateway:    newFakeGateway(t, ""),
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
