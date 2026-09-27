package cli

// `ticfac factory webhook` — register, inspect or withdraw the factory's
// Telegram webhook. Ported from ticks' cmd/tk/cmd/factory_webhook.go (deleted
// from ticks in pwp), with the body otherwise verbatim, exactly the way the
// cloud slice moved; the cobra plumbing that port dropped is back (tick nwj).
// This is the one factory operator surface pwp dropped without a replacement,
// and the factory is ticfac's now (tick glb).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// factoryWebhookPath is the factory route that administers webhook mode. The
// registration itself is a factory route, because the bot token is a Worker
// secret and the privacy-mode check and the deployment's own public URL both
// live there.
const factoryWebhookPath = "/api/channels/telegram/webhook/registration"

// newFactoryWebhookCommand builds `factory webhook`'s cobra command.
func newFactoryWebhookCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "point Telegram at the factory",
		Long:  "Register the factory's Telegram webhook by default; --status reads what\nTelegram believes it is, --delete withdraws it and hands the bot's updates\nback to polling.",
	}
	fs := newFlagSet("factory webhook", nil)
	var (
		delete = fs.Bool("delete", false, "withdraw the registration and hand the bot's updates back to polling")
		status = fs.Bool("status", false, "report what Telegram believes the webhook is, changing nothing")
		asJSON = fs.Bool("json", false, "print one versioned document (ticfac.factory-webhook.v1): the action and what Telegram believes the webhook is")
	)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(factoryWebhook(c.Context(), args, delete, status, asJSON, stdout, stderr))
	}
	return cmd
}

func factoryWebhook(ctx context.Context, args []string, delete, status *bool, asJSON *bool, stdout, stderr io.Writer) int {
	err := runFactoryWebhook(ctx, args, delete, status, asJSON, stdout, stderr)
	return reportCommand("factory webhook", err, stderr)
}

func runFactoryWebhook(ctx context.Context, args []string, delete, status *bool, asJSON *bool, stdout, stderr io.Writer) error {
	rest := args
	if len(rest) != 0 {
		return newExitError(exitUsage, "factory webhook takes no positional arguments")
	}
	if *delete && *status {
		return newExitError(exitUsage, "--delete withdraws the webhook and --status only reads it: pick one")
	}
	client, err := newCloudClient()
	if err != nil {
		return newExitError(exitUsage, "%v", err)
	}

	method := http.MethodPost
	switch {
	case *delete:
		method = http.MethodDelete
	case *status:
		method = http.MethodGet
	}

	data, err := client.request(ctx, method, factoryWebhookPath, nil)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	var report struct {
		URL                string   `json:"url"`
		AllowedUpdates     []string `json:"allowed_updates"`
		PrivacyMode        bool     `json:"privacy_mode"`
		Secret             bool     `json:"secret"`
		PendingUpdateCount int      `json:"pending_update_count"`
		LastErrorMessage   string   `json:"last_error_message"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return newExitError(exitGeneric, "the factory's answer could not be read: %v", err)
	}

	if *asJSON {
		action := "registered"
		if *delete {
			action = "withdrawn"
		} else if *status {
			action = "status"
		}
		doc := struct {
			agentDoc
			Action             string   `json:"action"`
			URL                string   `json:"url"`
			AllowedUpdates     []string `json:"allowed_updates"`
			PrivacyMode        bool     `json:"privacy_mode"`
			Secret             bool     `json:"secret"`
			PendingUpdateCount int      `json:"pending_update_count"`
			LastErrorMessage   string   `json:"last_error_message"`
		}{
			agentDoc:           agentDoc{Schema: agentSchemaID("factory-webhook"), State: agentStateDone},
			Action:             action,
			URL:                report.URL,
			AllowedUpdates:     report.AllowedUpdates,
			PrivacyMode:        report.PrivacyMode,
			Secret:             report.Secret,
			PendingUpdateCount: report.PendingUpdateCount,
			LastErrorMessage:   report.LastErrorMessage,
		}
		return emitAgentJSON(stdout, doc)
	}

	switch {
	case *delete:
		fmt.Fprintln(stdout, "Webhook withdrawn. The bot's updates are pollable again.")
	case report.URL == "":
		fmt.Fprintln(stdout, "No webhook is registered; the bot is in polling mode.")
	default:
		fmt.Fprintf(stdout, "Webhook: %s\n", report.URL)
		if len(report.AllowedUpdates) > 0 {
			fmt.Fprintf(stdout, "Updates: %v\n", report.AllowedUpdates)
		}
		if report.PrivacyMode {
			fmt.Fprintln(stdout, "Privacy mode: on (the bot sees only commands and replies to its own messages)")
		}
		if report.Secret {
			fmt.Fprintln(stdout, "Secret token: set (Telegram echoes it on every delivery)")
		}
		if report.PendingUpdateCount > 0 {
			fmt.Fprintf(stdout, "Pending updates: %d\n", report.PendingUpdateCount)
		}
		if report.LastErrorMessage != "" {
			fmt.Fprintf(stdout, "Last delivery error: %s\n", report.LastErrorMessage)
		}
	}
	return nil
}
