// Command classifiers re-asks recorded ticks' work-type question against any
// Workers AI decision model that speaks Jev's wire (tick r3y: Jev, Clef,
// Clef-flash). It sends exactly the production request — jev.Client.Classify,
// one tick per call, the state and question run-epic builds — and swaps only
// the body's model id, so the comparison is the model and nothing else.
//
// Input (stdin): one JSON tick per line, {id, title, description,
// acceptance_criteria, role, ...}; unknown fields are carried through.
// Output (stdout): one JSON line per tick with the distribution, the wall
// latency of the round trip, the answering model's name, usage, and, when
// the call gave no answer, why (with the raw body).
//
// The credential is read the way run-epic reads it (TICFAC_JEV_* env, else
// ~/.ticfacrc's Cloudflare token and the gateway URL's account). Nothing it
// prints carries the token or the account id.
//
//	go run ./benchmarks/classifiers -model @cf/cloudflare/clef-flash < ticks.jsonl
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/jev"
)

type tick struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	AC          string `json:"acceptance_criteria"`
	Role        string `json:"role"`
}

type out struct {
	ID            string             `json:"id"`
	AskedModel    string             `json:"asked_model"`
	Model         string             `json:"model,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	LatencyMS     float64            `json:"latency_ms"`
	Status        int                `json:"status"`
	Usage         jev.Usage          `json:"usage"`
	Shape         string             `json:"shape,omitempty"`
	Gap           string             `json:"gap,omitempty"`
	Raw           string             `json:"raw,omitempty"`
}

// shallow reads Clef's response shape, {"result": {"model", "answers",
// "usage"}}, which is Jev's minus the {state, result} wrapper.
func shallow(body []byte, id string, o *out) bool {
	var env struct {
		Result struct {
			Model   string `json:"model"`
			Answers map[string]struct {
				Choice        string             `json:"choice"`
				Confidence    float64            `json:"confidence"`
				Probabilities map[string]float64 `json:"probabilities"`
			} `json:"answers"`
			Usage jev.Usage `json:"usage"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil {
		return false
	}
	a, ok := env.Result.Answers[id]
	if !ok || len(a.Probabilities) == 0 {
		return false
	}
	o.Model, o.Usage = env.Result.Model, env.Result.Usage
	o.Choice, o.Confidence, o.Probabilities = a.Choice, a.Confidence, a.Probabilities
	return true
}

// swap rewrites the body's model id and times the round trip. One transport
// per call, so the recorded latency and body belong to that tick.
type swap struct {
	model   string
	latency time.Duration
	status  int
	body    []byte
	secret  []string
}

func (s *swap) RoundTrip(r *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if s.model != jev.Model {
		b = bytes.Replace(b, []byte(`"model":"`+jev.Model+`"`), []byte(`"model":"`+s.model+`"`), 1)
	}
	r = r.Clone(r.Context())
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	start := time.Now()
	resp, err := httpnet.Transport().RoundTrip(r)
	if err != nil {
		s.latency = time.Since(start)
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	s.latency = time.Since(start)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	s.status, s.body = resp.StatusCode, body
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func (s *swap) redact(text string) string {
	for _, secret := range s.secret {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	return text
}

func main() {
	model := flag.String("model", jev.Model, "Workers AI model id to ask")
	parallel := flag.Int("parallel", 1, "concurrent calls (1 keeps latency honest)")
	flag.Parse()

	var stored jev.Stored
	if file, err := credentials.Load(); err == nil {
		stored.APIToken = file.Get(credentials.KeyCloudflareAPIToken)
		if account, _, ok := gatewaytrace.GatewayIDs(file.Get(credentials.KeyGatewayURL)); ok {
			stored.AccountID = account
		}
	}
	src := jev.ResolveCredential(os.Getenv, stored)
	if !src.Configured {
		fmt.Fprintln(os.Stderr, "classifier not configured")
		os.Exit(2)
	}

	var ticks []tick
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var t tick
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			fmt.Fprintln(os.Stderr, "bad input line:", err)
			os.Exit(2)
		}
		ticks = append(ticks, t)
	}

	results := make([]out, len(ticks))
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	for i, t := range ticks {
		wg.Add(1)
		go func(i int, t tick) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tr := &swap{model: *model, secret: []string{src.Config.APIKey, src.Config.AccountID}}
			hc := httpnet.Client(90 * time.Second)
			hc.Transport = tr
			client := jev.New(src.Config, hc)
			o := out{ID: t.ID, AskedModel: *model}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			res, err := client.Classify(ctx, []jev.Tick{{ID: t.ID, Title: t.Title, Description: t.Description, AcceptanceCriteria: t.AC, Role: t.Role}})
			o.LatencyMS = float64(tr.latency.Microseconds()) / 1000
			o.Status = tr.status
			switch {
			case err != nil:
				o.Gap = tr.redact("error: " + err.Error())
			case res.Unavailable != "":
				// Clef answers one level SHALLOWER than Jev (result ->
				// {model, answers, usage}, no state), which the production
				// reader refuses. Read that shape here and say so.
				if ok := shallow(tr.body, t.ID, &o); ok {
					o.Shape = "result.answers (shallow; production reader refuses it)"
					break
				}
				o.Gap = tr.redact("unavailable: " + res.Unavailable)
				o.Raw = tr.redact(string(tr.body))
			default:
				o.Shape = "result.result.answers"
				o.Model, o.Usage = res.Model, res.Usage
				if c, ok := res.Classifications[t.ID]; ok {
					o.Choice, o.Confidence = string(c.Choice), c.Confidence
					o.Probabilities = map[string]float64{}
					for k, v := range c.Probabilities {
						o.Probabilities[string(k)] = v
					}
				} else {
					o.Gap = "unanswered: " + res.Unanswered[t.ID]
					o.Raw = tr.redact(string(tr.body))
				}
			}
			results[i] = o
			fmt.Fprintf(os.Stderr, "%s %s %.0fms %s\n", *model, t.ID, o.LatencyMS, o.Gap)
		}(i, t)
	}
	wg.Wait()
	enc := json.NewEncoder(os.Stdout)
	for _, o := range results {
		_ = enc.Encode(o)
	}
}
