package cli

// `ticfac cloud trace`, ported from ticks' cmd/tk/cmd/cloud_trace.go with the
// cobra plumbing replaced by a flag set and the body otherwise verbatim.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
)

// cloudTraceHTTPClient is a package variable so command tests can exercise the
// AI Gateway logs protocol without binding a loopback listener.
var cloudTraceHTTPClient = &http.Client{Timeout: 30 * time.Second}

// cloudTraceDetailWorkers bounds how many detail bodies are read at once. A run
// makes tens to hundreds of model calls and each needs its own request, so this
// is the difference between a trace that returns and one an operator gives up
// on; it is small enough not to look like an attack on the operator's own API.
const cloudTraceDetailWorkers = 6

func runCloudTrace(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := cloudTrace(ctx, args, stdout, stderr)
	return reportCommand("cloud trace", err, stderr)
}

func cloudTrace(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("cloud trace", stderr)
	asJSON := fs.Bool("json", false, "emit the raw gateway log rows (or one call's raw bodies with --call)")
	call := fs.Int("call", 0, "dump one exchange in full, by its 1-based call number")
	tools := fs.Bool("tools", false, "list only the tool calls and their arguments")
	cache := fs.Bool("cache", false, "per-call prefix-cache table: input tokens, cached tokens, hit rate")
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) != 1 || rest[0] == "" {
		return newExitError(exitUsage, "exactly one run id is required")
	}

	// Views are refused in combination rather than silently ranked: an
	// operator who asked for two things and got one has been answered wrongly.
	views := 0
	for _, on := range []bool{*tools, *cache, *call > 0} {
		if on {
			views++
		}
	}
	if views > 1 {
		return newExitError(exitGeneric, "--call, --tools and --cache each select a different view; use one at a time")
	}
	if *call < 0 {
		return newExitError(exitGeneric, "--call takes a 1-based call number, got %d", *call)
	}

	config, err := factory.LoadCredentials()
	if err != nil {
		return newExitError(exitGeneric, "cannot read factory configuration: %v", err)
	}
	traceConfig, err := gatewaytrace.ConfigFrom(config)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if cloudTraceHTTPClient == nil {
		cloudTraceHTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := gatewaytrace.New(traceConfig, cloudTraceHTTPClient)

	// Before the gateway is asked anything: it can only answer about the exact
	// string it is given, and its answer for a prefix is a negative that reads
	// as a verdict on the run (tick c5i).
	runID, err := cloudRunIDArg(ctx, rest[0], stderr)
	if err != nil {
		return err
	}
	calls, err := client.Calls(ctx, runID)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if len(calls) == 0 {
		if *asJSON {
			return writeCloudTraceJSON(stdout, map[string]any{
				"run_id": runID, "totals": gatewaytrace.Sum(calls), "calls": []any{},
			})
		}
		// Two different facts, and the operator needs to know which: a run that
		// made no model calls, or a run id that never existed here.
		fmt.Fprintf(stdout, "No AI Gateway calls are stamped with run %s.\n", runID)
		fmt.Fprintln(stdout, "  Either the run made no model call, or that is not a run id this gateway proxied.")
		return nil
	}

	switch {
	case *call > 0:
		return cloudTraceOneCall(ctx, client, runID, calls, *call, *asJSON, stdout)
	case *cache:
		return cloudTraceCacheView(stdout, runID, calls)
	case *asJSON:
		return cloudTraceJSONRows(stdout, runID, calls)
	}

	conversation, missed, err := cloudTraceConversation(ctx, client, calls)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if *tools {
		cloudTraceToolsView(stdout, runID, conversation, missed)
		return nil
	}
	cloudTraceSummary(stdout, runID, calls)
	fmt.Fprintln(stdout)
	cloudTraceConversationView(stdout, conversation, missed)
	return nil
}

// cloudTraceConversation reads every call's request body and reconstructs the
// conversation from it. Response bodies are deliberately not read: they are
// streamed and therefore empty, so reading them would double the request count
// to learn nothing.
func cloudTraceConversation(
	ctx context.Context,
	client *gatewaytrace.Client,
	calls []gatewaytrace.Call,
) ([]gatewaytrace.Message, []int, error) {
	type result struct {
		index int
		body  json.RawMessage
		err   error
	}

	results := make([]result, len(calls))
	var wait sync.WaitGroup
	work := make(chan int)
	for worker := 0; worker < cloudTraceDetailWorkers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := range work {
				body, err := client.RequestBody(ctx, calls[i].ID)
				results[i] = result{index: i, body: body, err: err}
			}
		}()
	}
	for i := range calls {
		work <- i
	}
	close(work)
	wait.Wait()

	requests := make([]gatewaytrace.CallRequest, 0, len(calls))
	missed := make([]int, 0)
	for i, item := range results {
		if item.err != nil || len(item.body) == 0 {
			// Named, never dropped: a conversation silently missing a call's
			// turns reads as a model that went quiet.
			missed = append(missed, calls[i].Index)
			continue
		}
		requests = append(requests, gatewaytrace.CallRequest{Call: calls[i].Index, Body: item.body})
	}
	sort.Ints(missed)

	conversation, err := gatewaytrace.Reconstruct(requests)
	if err != nil {
		return nil, missed, err
	}
	return conversation, missed, nil
}

func cloudTraceSummary(out io.Writer, runID string, calls []gatewaytrace.Call) {
	totals := gatewaytrace.Sum(calls)
	fmt.Fprintf(out, "Cloud run %s — %s\n", runID, plural(totals.Calls, "model call", "model calls"))
	cloudTraceIDLine(out, calls)
	fmt.Fprintf(out, "  tokens: %s in / %s out / %s cached (%s of input)\n",
		humanInt(totals.TokensIn), humanInt(totals.TokensOut), humanInt(totals.CachedTokens),
		rateText(totals.CacheRate()))
	fmt.Fprintf(out, "  cost: %s\n", costText(totals.Cost))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %3s  %-8s  %-22s %9s %8s %9s %7s %10s\n",
		"#", "time", "model", "in", "out", "cached", "cache%", "cost")
	for _, call := range calls {
		fmt.Fprintf(out, "  %3d  %-8s  %-22s %9s %8s %9s %7s %10s\n",
			call.Index, clockText(call), truncate(call.Model, 22),
			humanInt(call.TokensIn), humanInt(call.TokensOut), humanInt(call.CachedTokens),
			rateText(call.CacheRate()), costText(call.Cost))
	}
}

// cloudTraceCacheView answers the caching question at a glance: prefix caching
// is per model instance and invalidated by a single changed token near the head
// of the prompt, so the per-call rate — not the average — is what shows whether
// it is actually hitting.
func cloudTraceCacheView(out io.Writer, runID string, calls []gatewaytrace.Call) error {
	totals := gatewaytrace.Sum(calls)
	fmt.Fprintf(out, "Cloud run %s — prefix cache over %s\n", runID, plural(totals.Calls, "model call", "model calls"))
	fmt.Fprintf(out, "  overall: %s of %s input tokens cached (%s)\n",
		humanInt(totals.CachedTokens), humanInt(totals.TokensIn), rateText(totals.CacheRate()))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %3s  %-8s %9s %9s %7s\n", "#", "time", "in", "cached", "cache%")
	for _, call := range calls {
		fmt.Fprintf(out, "  %3d  %-8s %9s %9s %7s\n",
			call.Index, clockText(call), humanInt(call.TokensIn), humanInt(call.CachedTokens),
			rateText(call.CacheRate()))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  cached tokens come from the log row's usage_metadata.input_cached_tokens;")
	fmt.Fprintln(out, "  a 0% call means the prompt prefix changed, not that the cache was cold.")
	return nil
}

func cloudTraceToolsView(out io.Writer, runID string, conversation []gatewaytrace.Message, missed []int) {
	tools := gatewaytrace.ToolCalls(conversation)
	fmt.Fprintf(out, "Cloud run %s — %s\n", runID, plural(len(tools), "tool call", "tool calls"))
	cloudTraceMissedNote(out, missed)
	for _, tool := range tools {
		fmt.Fprintf(out, "  [call %d] %s %s\n", tool.Call, tool.Name, truncate(oneLine(tool.Arguments), 160))
	}
}

func cloudTraceConversationView(out io.Writer, conversation []gatewaytrace.Message, missed []int) {
	fmt.Fprintf(out, "Conversation — %s reconstructed from request bodies\n",
		plural(len(conversation), "message", "messages"))
	fmt.Fprintln(out, "  (responses are streamed, so the logged response bodies carry no content)")
	cloudTraceMissedNote(out, missed)
	for i, message := range conversation {
		fmt.Fprintf(out, "  [%d] %-9s call %d  %s\n", i+1, message.Role, message.Call,
			truncate(oneLine(message.Text), 120))
		for _, tool := range message.ToolCalls {
			fmt.Fprintf(out, "      → %s %s\n", tool.Name, truncate(oneLine(tool.Arguments), 120))
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  use --call N for one exchange in full, --tools for the tool calls, --cache for cache rates")
}

func cloudTraceMissedNote(out io.Writer, missed []int) {
	if len(missed) == 0 {
		return
	}
	text := make([]string, 0, len(missed))
	for _, index := range missed {
		text = append(text, fmt.Sprint(index))
	}
	noun := "calls"
	if len(missed) == 1 {
		noun = "call"
	}
	fmt.Fprintf(out, "  incomplete: the request body for %s %s could not be read, so those turns are missing\n",
		noun, strings.Join(text, ", "))
}

func cloudTraceOneCall(ctx context.Context, client *gatewaytrace.Client, runID string,
	calls []gatewaytrace.Call, callNumber int, asJSON bool, out io.Writer) error {
	if callNumber > len(calls) {
		return newExitError(exitGeneric, "run %s made %d model calls, so there is no call %d",
			runID, len(calls), callNumber)
	}
	call := calls[callNumber-1]

	request, requestErr := client.RequestBody(ctx, call.ID)
	response, responseErr := client.ResponseBody(ctx, call.ID)

	if asJSON {
		payload := map[string]any{"run_id": runID, "call": call.Index, "id": call.ID, "row": call.Raw}
		if requestErr == nil {
			payload["request"] = request
		} else {
			payload["request_error"] = requestErr.Error()
		}
		if responseErr == nil {
			payload["response"] = response
		} else {
			payload["response_error"] = responseErr.Error()
		}
		return writeCloudTraceJSON(out, payload)
	}

	fmt.Fprintf(out, "Cloud run %s — call %d of %d\n", runID, call.Index, len(calls))
	fmt.Fprintf(out, "  id: %s\n", call.ID)
	fmt.Fprintf(out, "  time: %s\n", call.CreatedAt)
	fmt.Fprintf(out, "  model: %s (%s)\n", call.Model, call.Provider)
	if call.TickID != "" {
		fmt.Fprintf(out, "  tick: %s\n", call.TickID)
	}
	if call.TraceID != "" {
		fmt.Fprintf(out, "  trace: %s\n", call.TraceID)
	}
	fmt.Fprintf(out, "  tokens: %s in / %s out / %s cached (%s)\n",
		humanInt(call.TokensIn), humanInt(call.TokensOut), humanInt(call.CachedTokens),
		rateText(call.CacheRate()))
	fmt.Fprintf(out, "  cost: %s\n", costText(call.Cost))
	fmt.Fprintln(out)

	if requestErr != nil {
		fmt.Fprintf(out, "Request body could not be read: %v\n", requestErr)
	} else {
		messages, err := gatewaytrace.ParseMessages(request)
		if err != nil {
			return newExitError(exitGeneric, "%v", err)
		}
		fmt.Fprintf(out, "Request messages (%d)\n", len(messages))
		for i, message := range messages {
			fmt.Fprintf(out, "  [%d] %s\n", i+1, message.Role)
			if message.ToolCallID != "" {
				fmt.Fprintf(out, "      answering tool call %s\n", message.ToolCallID)
			}
			if message.Text != "" {
				fmt.Fprintln(out, indent(message.Text, "      "))
			}
			for _, tool := range message.ToolCalls {
				fmt.Fprintf(out, "      → %s %s\n", tool.Name, tool.Arguments)
			}
		}
	}

	fmt.Fprintln(out)
	if responseErr != nil {
		fmt.Fprintf(out, "Response body could not be read: %v\n", responseErr)
		return nil
	}
	fmt.Fprintln(out, "Response body (streamed: choices carry no content — the model's turn is in the NEXT call's request)")
	fmt.Fprintln(out, indent(prettyJSON(response), "  "))
	return nil
}

// cloudTraceIDLine states the chain this run's model traffic belongs to.
//
// Read off the CALLS rather than asked of the factory, because this command
// deliberately talks to the operator's own AI Gateway and to nothing else —
// the whole reason it can answer for a run whose control plane is unreachable.
// The gateway stamps the trace id on every proxied request out of the run row
// (cloud/factory/src/gateway.ts), so the calls carry it.
//
// Disagreement is REPORTED, never averaged away: two trace ids on one run id
// means something stamped the wrong chain, and quietly showing the first would
// hide the one bug this identifier exists to make impossible.
func cloudTraceIDLine(out io.Writer, calls []gatewaytrace.Call) {
	seen := make([]string, 0, 2)
	for _, call := range calls {
		if call.TraceID == "" || slices.Contains(seen, call.TraceID) {
			continue
		}
		seen = append(seen, call.TraceID)
	}
	switch len(seen) {
	case 0:
		// Nothing rather than "trace: none": every run before tick hyi has no
		// chain, and a line that always prints is a line that never answers.
	case 1:
		fmt.Fprintf(out, "  trace: %s\n", seen[0])
	default:
		sort.Strings(seen)
		fmt.Fprintf(out, "  trace: %s — MORE THAN ONE CHAIN is stamped on this run's calls; "+
			"one run is one chain, so this is a bug in what stamped them\n", strings.Join(seen, ", "))
	}
}

func cloudTraceJSONRows(out io.Writer, runID string, calls []gatewaytrace.Call) error {
	rows := make([]json.RawMessage, 0, len(calls))
	for _, call := range calls {
		rows = append(rows, call.Raw)
	}
	return writeCloudTraceJSON(out, map[string]any{
		"run_id": runID,
		"totals": gatewaytrace.Sum(calls),
		"calls":  rows,
	})
}

func writeCloudTraceJSON(out io.Writer, payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return newExitError(exitGeneric, "encode trace: %v", err)
	}
	fmt.Fprintln(out, string(encoded))
	return nil
}

// ------------------------------------------------------------ formatting ---

func clockText(call gatewaytrace.Call) string {
	if call.Time.IsZero() {
		return truncate(call.CreatedAt, 8)
	}
	return call.Time.UTC().Format("15:04:05")
}

func rateText(rate float64, ok bool) string {
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

func costText(cost float64) string {
	if cost == 0 {
		return "$0"
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.5f", cost)
	}
	return fmt.Sprintf("$%.4f", cost)
}

// humanInt groups thousands, because a token count is read for its magnitude
// and 999040 next to 99904 is a comparison nobody makes correctly at a glance.
func humanInt(value int) string {
	text := fmt.Sprint(value)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	var grouped strings.Builder
	for i, digit := range text {
		if i > 0 && (len(text)-i)%3 == 0 {
			grouped.WriteRune(',')
		}
		grouped.WriteRune(digit)
	}
	if negative {
		return "-" + grouped.String()
	}
	return grouped.String()
}

func plural(count int, one, many string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, one)
	}
	return fmt.Sprintf("%d %s", count, many)
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func prettyJSON(raw json.RawMessage) string {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	if err := encoder.Encode(value); err != nil {
		return string(raw)
	}
	return strings.TrimRight(buffer.String(), "\n")
}
