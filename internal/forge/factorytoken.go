package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The factory's GitHub token door, as the forge inside a cloud container
// speaks to it (epic dm6).
//
// On the factory's GitHub App rung a write-grade container boots with an
// installation token minted for its one repository, and GitHub kills that
// token an hour later. A run lives up to six. So the control plane also
// hands the container TICKS_GITHUB_TOKEN_URL — the Worker's
// `POST /api/github/token` — which answers the run's CURRENT token when asked
// with the run's own credential (TICKS_FACTORY_TOKEN). The image's git
// credential helper asks it before every push; this is the same question for
// the REST calls the close-out makes (open the PR, read CI, re-run a job).

// FactoryTokenURLEnv names the door; unset means the token the container
// booted with is the only one it will get (the PAT and device-flow rungs).
const FactoryTokenURLEnv = "TICKS_GITHUB_TOKEN_URL"

// FactoryRunTokenEnv is the run's own credential the door authenticates.
const FactoryRunTokenEnv = "TICKS_FACTORY_TOKEN"

// factoryTokenMargin is how long before its expiry a held token is asked
// for again — the same margin the Worker caches to, so the two agree about
// when a token is "about to die".
const factoryTokenMargin = 5 * time.Minute

// FactoryTokenSourceFromEnv answers a Refresh for GitHub when this process
// runs where the factory's token door is configured, and nil otherwise.
func FactoryTokenSourceFromEnv() func(context.Context) (string, error) {
	url := strings.TrimSpace(os.Getenv(FactoryTokenURLEnv))
	runToken := strings.TrimSpace(os.Getenv(FactoryRunTokenEnv))
	if url == "" || runToken == "" {
		return nil
	}
	return FactoryTokenSource(url, runToken, nil, nil)
}

// FactoryTokenSource asks the factory's token door for the run's current
// GitHub token, holding each answer until factoryTokenMargin before it
// expires, so a close-out's burst of calls costs one request rather than one
// each.
func FactoryTokenSource(url, runToken string, client *http.Client, now func() time.Time) func(context.Context) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	var (
		mu      sync.Mutex
		held    string
		expires time.Time
	)
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if held != "" && expires.Sub(now()) > factoryTokenMargin {
			return held, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+runToken)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("the factory's GitHub token door did not answer: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode != http.StatusOK {
			var refusal struct {
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(body, &refusal)
			return "", fmt.Errorf("the factory's GitHub token door answered %d: %s", resp.StatusCode, firstLine(refusal.Detail))
		}
		var answer struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(body, &answer); err != nil || answer.Token == "" {
			return "", fmt.Errorf("the factory's GitHub token door answered no token")
		}
		held = answer.Token
		if expires, err = time.Parse(time.RFC3339, answer.ExpiresAt); err != nil {
			// No usable expiry: use it for this call, and ask again next time.
			expires = time.Time{}
		}
		return held, nil
	}
}
