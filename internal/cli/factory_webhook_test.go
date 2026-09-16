package cli

// Command tests for `ticfac factory webhook`, ported from the shape of the
// cloud command tests: the factory is a round tripper, so the assertions are
// on the method and path each flag maps to, and on the human report each
// answer renders.

import (
	"bytes"
	"strings"
	"testing"
)

func TestFactoryWebhookRegistersByDefault(t *testing.T) {
	endpoint, requests := newCloudFactory(t, func(r cloudFactoryRequest) (int, any) {
		if r.Method != "POST" || r.Path != "/api/channels/telegram/webhook/registration" {
			t.Fatalf("request: %s %s", r.Method, r.Path)
		}
		if r.Auth != "Bearer tkf_test-token" {
			t.Fatalf("auth: %q", r.Auth)
		}
		return 200, map[string]any{
			"url":                  "https://factory.test/api/channels/telegram/webhook",
			"allowed_updates":      []string{"callback_query", "message"},
			"privacy_mode":         true,
			"secret":               true,
			"pending_update_count": 2,
			"last_error_message":   "a test error",
		}
	})
	configureCloudFactory(t, endpoint)

	var out, errOut bytes.Buffer
	if code := Run([]string{"factory", "webhook"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	report := out.String()
	for _, want := range []string{
		"Webhook: https://factory.test/api/channels/telegram/webhook",
		"Privacy mode: on",
		"Secret token: set",
		"Pending updates: 2",
		"Last delivery error: a test error",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("requests: %+v", *requests)
	}
}

func TestFactoryWebhookStatus(t *testing.T) {
	endpoint, requests := newCloudFactory(t, func(r cloudFactoryRequest) (int, any) {
		if r.Method != "GET" {
			t.Fatalf("status reads: %s %s", r.Method, r.Path)
		}
		return 200, map[string]any{"url": "", "pending_update_count": 0}
	})
	configureCloudFactory(t, endpoint)

	var out, errOut bytes.Buffer
	if code := Run([]string{"factory", "webhook", "--status"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No webhook is registered; the bot is in polling mode.") {
		t.Fatalf("report: %s", out.String())
	}
	if len(*requests) != 1 {
		t.Fatalf("requests: %+v", *requests)
	}
}

func TestFactoryWebhookDelete(t *testing.T) {
	endpoint, requests := newCloudFactory(t, func(r cloudFactoryRequest) (int, any) {
		if r.Method != "DELETE" {
			t.Fatalf("delete withdraws: %s %s", r.Method, r.Path)
		}
		return 200, map[string]any{"ok": true, "url": ""}
	})
	configureCloudFactory(t, endpoint)

	var out, errOut bytes.Buffer
	if code := Run([]string{"factory", "webhook", "--delete"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Webhook withdrawn. The bot's updates are pollable again.") {
		t.Fatalf("report: %s", out.String())
	}
	if len(*requests) != 1 {
		t.Fatalf("requests: %+v", *requests)
	}
}

func TestFactoryWebhookRefusesFlagCombination(t *testing.T) {
	endpoint, _ := newCloudFactory(t, func(r cloudFactoryRequest) (int, any) {
		t.Fatal("nothing may be called for a usage refusal")
		return 200, nil
	})
	configureCloudFactory(t, endpoint)

	var out, errOut bytes.Buffer
	if code := Run([]string{"factory", "webhook", "--delete", "--status"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want usage", code)
	}
	if !strings.Contains(errOut.String(), "pick one") {
		t.Fatalf("the refusal says what is wrong: %s", errOut.String())
	}
}

func TestFactoryWebhookWithoutConfiguredFactory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out, errOut bytes.Buffer
	if code := Run([]string{"factory", "webhook"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want tk's usage code for an unconfigured factory", code)
	}
	if !strings.Contains(errOut.String(), "no factory is configured") {
		t.Fatalf("the refusal names the remedy: %s", errOut.String())
	}
}

func TestFactoryWebhookInGroupUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"factory"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want usage", code)
	}
	if !strings.Contains(errOut.String(), "webhook") {
		t.Fatalf("the group's help names webhook: %s", errOut.String())
	}
}
