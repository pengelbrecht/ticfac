package factory

// The claude-sub pool's operator read (tick b13): GET /api/claude-sub, the
// route the factory serves under its own bearer check (tick 6fv,
// cloudflare/src/claude-sub.ts) — the same auth `ticfac factory status` uses,
// because the caller is the operator reading what their subscriptions are
// doing, never a run. The answer carries labels, leases and limit headers and
// never a token value, so it is safe to hold in a dashboard's memory.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// claudeSubPath is the factory route that answers the pool's snapshot.
const claudeSubPath = "/api/claude-sub"

// ErrNoClaudeSubPool is the answer of a factory that has no claude-sub pool
// to read: no CLAUDE_SUB_TOKEN secret is configured (the rung is off, every
// lease falls back to Workers AI) or the deployment predates the route. A
// caller treats it as the optional state — nothing to show — never as a
// failed read.
var ErrNoClaudeSubPool = errors.New("this factory has no claude-sub pool")

// ClaudeSubSnapshot is /api/claude-sub's answer: the subscriptions this
// deployment has a token secret for, each with its leases and the limit
// headers its proxy last saw.
type ClaudeSubSnapshot struct {
	Labels        []string        `json:"labels"`
	Subscriptions []ClaudeSubView `json:"subscriptions"`
}

// ClaudeSubView is one subscription's state, the fields the pool's own
// SubscriptionView carries that a reader outside the factory reads. The
// token itself never travels — the route is built so the whole answer is
// safe to paste into a log (tick 6fv).
type ClaudeSubView struct {
	Label string `json:"label"`
	// ActiveLeases is the job ids currently holding a lease. A job's id is
	// the sandbox it runs in, spelled `<run-id>-<tick>-<attempt>` (or with a
	// trailing slot), so a lease of THIS run's begins with its run id.
	ActiveLeases []string `json:"active_leases"`
	// LastLimits is the last `anthropic-ratelimit-unified-*` headers the
	// proxy saw, verbatim — the window utilization a claude-sub run reads
	// its spend story from (docs/spikes/jvj-claude-sub-cloud.md: after real
	// review jobs the 5h/7d utilization headers carried decimal fractions).
	LastLimits map[string]string `json:"last_limits"`
}

// FetchClaudeSub asks the factory what its claude-sub pool looks like. A nil
// client gets the ordinary bounded one; the URL is the factory base the
// operator's config already carries (no trailing slash).
func FetchClaudeSub(ctx context.Context, client *http.Client, factoryURL, token string) (*ClaudeSubSnapshot, error) {
	if factoryURL == "" || token == "" {
		return nil, ErrNoClaudeSubPool
	}
	if client == nil {
		client = httpnet.Client(15 * time.Second)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(factoryURL, "/")+claudeSubPath, nil)
	if err != nil {
		return nil, err
	}
	// Operator auth, the same bearer check every /api route sits behind.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", claudeSubPath, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// A factory that binds no pool answers 503 with its own error word, and
	// one that predates the route answers 404 — both are "nothing to show",
	// the optional state, not a failure. Every other status is one.
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable:
		var refused struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &refused)
		if refused.Error == "no_claude_sub_pool" {
			return nil, ErrNoClaudeSubPool
		}
		return nil, fmt.Errorf("GET %s answered %s: %s", claudeSubPath, resp.Status, firstLine(body))
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoClaudeSubPool
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s answered %s: %s", claudeSubPath, resp.Status, firstLine(body))
	}
	var snapshot ClaudeSubSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, fmt.Errorf("GET %s answered something that is not the pool's snapshot: %w", claudeSubPath, err)
	}
	return &snapshot, nil
}

// LeasedLabels returns the snapshot's subscriptions whose active leases name
// one of run's jobs, in the snapshot's own order — the labels sorted — so a
// caller that takes the first takes a deterministic one. A lease belongs to
// the run when its job id IS the run id or begins with it and a separator:
// every job id the factory mints is a sandbox name built from the run id
// (`<run-id>-<tick>-<attempt>`, sandbox.ts / sandbox-executor.ts), and the
// separator keeps `run_abc` from claiming a run `run_abc2`'s lease.
func (s *ClaudeSubSnapshot) LeasedLabels(runID string) []string {
	if s == nil || runID == "" {
		return nil
	}
	prefix := runID + "-"
	var leased []string
	for _, view := range s.Subscriptions {
		for _, jobID := range view.ActiveLeases {
			if jobID == runID || strings.HasPrefix(jobID, prefix) {
				leased = append(leased, view.Label)
				break
			}
		}
	}
	return leased
}

// ViewOf returns the snapshot's entry for one label, or nil when the
// snapshot does not name it — a label the factory has no token for is a
// label whose windows nobody can read.
func (s *ClaudeSubSnapshot) ViewOf(label string) *ClaudeSubView {
	if s == nil {
		return nil
	}
	for i, view := range s.Subscriptions {
		if view.Label == label {
			return &s.Subscriptions[i]
		}
	}
	return nil
}

// claudeSubUtilizationHeaders are the unified limit headers the proxy
// records verbatim (claude-sub.ts limitHeaders) that carry a window's
// utilization: Anthropic spells them as decimal fractions of the window
// (docs/spikes/jvj-claude-sub-cloud.md, measured on staging: "5h utilization
// 0.12" after four parallel opus jobs).
const (
	claudeSub5hHeader = "anthropic-ratelimit-unified-5h-utilization"
	claudeSub7dHeader = "anthropic-ratelimit-unified-7d-utilization"
)

// WindowUtilization reads the subscription's window utilization out of the
// limit headers its proxy last recorded: the fraction of the 5-hour window,
// the fraction of the 7-day window, nil per window the proxy has answered
// nothing for yet — no header, no number, never a guess. A value outside
// 0–1 is refused the same way: a header that cannot be read is not a
// measurement.
func (v *ClaudeSubView) WindowUtilization() (fiveHour, sevenDay *float64) {
	return utilizationOf(v.LastLimits, claudeSub5hHeader), utilizationOf(v.LastLimits, claudeSub7dHeader)
}

// utilizationOf parses one utilization header. A decimal fraction ("0.12")
// is the spelling the real headers carry; a percent spelling ("12%") is
// accepted beside it so a vendor change of units degrades to the same
// number rather than to nothing.
func utilizationOf(limits map[string]string, header string) *float64 {
	raw, ok := limits[header]
	if !ok {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if percent, ok := strings.CutSuffix(raw, "%"); ok {
		value, err := strconv.ParseFloat(strings.TrimSpace(percent), 64)
		if err != nil {
			return nil
		}
		return windowFraction(value / 100)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return windowFraction(value)
}

// windowFraction accepts a fraction only inside the range a utilization can
// have: 0 through 1 inclusive. Anything else is a header this reader does
// not understand, and an unreadable header states no number.
func windowFraction(value float64) *float64 {
	if value < 0 || value > 1 {
		return nil
	}
	return &value
}
